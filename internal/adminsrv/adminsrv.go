// Package adminsrv is a small HTTP API for managing ACME DNS-01 challenge TXT
// records. Bind it privately; sensitive endpoints use constant-time bearer-token auth.
package adminsrv

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/viaveritas/opendns/internal/txtstore"
)

type challengeBody struct {
	FQDN  string `json:"fqdn"`
	Value string `json:"value"`
	TTL   int    `json:"ttl"` // seconds; defaults to 120 if zero
}

// Handler exposes the admin endpoints. When token is non-empty, /acme-challenge
// and /debug/txt require "Authorization: Bearer <token>"; /healthz stays open for liveness probes.
func Handler(store txtstore.Store, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/debug/txt", authed(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap, err := store.Snapshot()
		if err != nil {
			http.Error(w, "store unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})))
	mux.Handle("/acme-challenge", authed(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleSet(store, w, r)
		case http.MethodDelete:
			handleDelete(store, w, r)
		default:
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})))
	return mux
}

// authed runs h only for requests with a matching bearer token; empty token disables auth.
func authed(token string, h http.Handler) http.Handler {
	if token == "" {
		return h
	}
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="opendns-admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		got := []byte(auth[len(prefix):])
		if subtle.ConstantTimeEq(int32(len(got)), int32(len(expected))) != 1 ||
			subtle.ConstantTimeCompare(got, expected) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// maxTTL bounds a record's lifetime so a caller can't pin a TXT value or grow memory unboundedly.
const maxTTL = 24 * time.Hour

func handleSet(store txtstore.Store, w http.ResponseWriter, r *http.Request) {
	var b challengeBody
	if err := decode(w, r, &b); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validate(b); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ttl := time.Duration(b.TTL) * time.Second
	if ttl <= 0 {
		ttl = 120 * time.Second
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	if err := store.Set(b.FQDN, b.Value, ttl); err != nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleDelete(store txtstore.Store, w http.ResponseWriter, r *http.Request) {
	var b challengeBody
	if err := decode(w, r, &b); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validate(b); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := store.Delete(b.FQDN, b.Value); err != nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	// Cap the body so a caller can't force a multi-GB allocation.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func validate(b challengeBody) error {
	fqdn := strings.TrimSpace(b.FQDN)
	if fqdn == "" {
		return errBadRequest("fqdn is required")
	}
	if len(fqdn) > 253 {
		return errBadRequest("fqdn too long (max 253)")
	}
	value := strings.TrimSpace(b.Value)
	if value == "" {
		return errBadRequest("value is required")
	}
	// DNS-01 values are ~43-char base64url SHA-256 digests; 512 bounds a TXT record with headroom.
	if len(value) > 512 {
		return errBadRequest("value too long (max 512)")
	}
	return nil
}

type errBadRequest string

func (e errBadRequest) Error() string { return string(e) }
