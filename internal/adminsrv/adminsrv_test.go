package adminsrv

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/viaveritas/opendns/internal/txtstore"
)

func do(t *testing.T, h http.Handler, method, path, body, authz string) *http.Response {
	t.Helper()
	var br *bytes.Reader
	if body != "" {
		br = bytes.NewReader([]byte(body))
	} else {
		br = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, br)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Result()
}

func TestAuthDisabledWhenTokenEmpty(t *testing.T) {
	h := Handler(txtstore.NewMem(), "")
	body := `{"fqdn":"_acme-challenge.test.","value":"v"}`
	if r := do(t, h, http.MethodPost, "/acme-challenge", body, ""); r.StatusCode != http.StatusNoContent {
		t.Fatalf("no-token mode: POST got %d, want 204", r.StatusCode)
	}
}

func TestAuthRequiredWhenTokenSet(t *testing.T) {
	h := Handler(txtstore.NewMem(), "s3cret")
	body := `{"fqdn":"_acme-challenge.test.","value":"v"}`

	if r := do(t, h, http.MethodPost, "/acme-challenge", body, ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no header: got %d, want 401", r.StatusCode)
	}
	if r := do(t, h, http.MethodPost, "/acme-challenge", body, "Bearer wrong"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", r.StatusCode)
	}
	if r := do(t, h, http.MethodPost, "/acme-challenge", body, "Bearer s3cret"); r.StatusCode != http.StatusNoContent {
		t.Fatalf("correct token: got %d, want 204", r.StatusCode)
	}
	// Bearer is case-insensitive per RFC 7235 §2.1.
	if r := do(t, h, http.MethodPost, "/acme-challenge", body, "bearer s3cret"); r.StatusCode != http.StatusNoContent {
		t.Fatalf("lowercase scheme: got %d, want 204", r.StatusCode)
	}
}

func TestDebugTxtAlsoGated(t *testing.T) {
	store := txtstore.NewMem()
	h := Handler(store, "s3cret")

	if r := do(t, h, http.MethodGet, "/debug/txt", "", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth /debug/txt: got %d, want 401", r.StatusCode)
	}
	if r := do(t, h, http.MethodGet, "/debug/txt", "", "Bearer s3cret"); r.StatusCode != http.StatusOK {
		t.Fatalf("auth /debug/txt: got %d, want 200", r.StatusCode)
	}
}

func TestHealthzAlwaysOpen(t *testing.T) {
	h := Handler(txtstore.NewMem(), "s3cret")
	if r := do(t, h, http.MethodGet, "/healthz", "", ""); r.StatusCode != http.StatusOK {
		t.Fatalf("healthz: got %d, want 200", r.StatusCode)
	}
}

func TestUnauthorizedAdvertisesBearer(t *testing.T) {
	h := Handler(txtstore.NewMem(), "s3cret")
	r := do(t, h, http.MethodPost, "/acme-challenge", `{}`, "")
	if !strings.Contains(r.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("missing WWW-Authenticate: %q", r.Header.Get("WWW-Authenticate"))
	}
}
