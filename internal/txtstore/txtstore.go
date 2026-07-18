// Package txtstore stores TXT records keyed by FQDN, sized for ACME DNS-01
// challenges (short-lived, multiple values per FQDN per RFC 8555 §8.4). MemStore
// (NewMem) is in-process for single instances; RedisStore (NewRedis) is shared so
// multiple instances behind the same NS delegation serve the same records.
package txtstore

import (
	"strings"
	"time"
)

// Store is the operation set the DNS handler and admin API need. Methods return
// an error so a remote backend can report failures; MemStore always returns nil.
type Store interface {
	// Set upserts value under fqdn with ttl; re-setting an existing pair refreshes expiry, not duplicates.
	Set(fqdn, value string, ttl time.Duration) error
	// Delete removes a single (fqdn, value). It is a no-op if not found.
	Delete(fqdn, value string) error
	// Get returns the live (unexpired) values for fqdn.
	Get(fqdn string) ([]string, error)
	// Snapshot returns all live entries keyed by FQDN — for /debug/txt.
	Snapshot() (map[string][]string, error)
}

// key normalises an FQDN: lower-case and ensure trailing dot.
func key(s string) string {
	s = strings.ToLower(s)
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}
