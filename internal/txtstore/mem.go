package txtstore

import (
	"sync"
	"time"
)

type entry struct {
	value   string
	expires time.Time
}

// MemStore is an in-process Store, default for single-instance deployments; use NewRedis for multiple.
type MemStore struct {
	mu  sync.RWMutex
	m   map[string][]entry
	now func() time.Time
}

// NewMem returns an empty in-memory store using the wall clock.
func NewMem() *MemStore {
	return &MemStore{m: map[string][]entry{}, now: time.Now}
}

// newMemWithClock is for tests.
func newMemWithClock(now func() time.Time) *MemStore {
	return &MemStore{m: map[string][]entry{}, now: now}
}

// Set upserts value under fqdn with ttl; an existing pair's expiry is refreshed, not duplicated.
func (s *MemStore) Set(fqdn, value string, ttl time.Duration) error {
	k := key(fqdn)
	exp := s.now().Add(ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[k]
	for i := range list {
		if list[i].value == value {
			list[i].expires = exp
			return nil
		}
	}
	s.m[k] = append(list, entry{value: value, expires: exp})
	return nil
}

// Delete removes a single (fqdn, value). It is a no-op if not found.
func (s *MemStore) Delete(fqdn, value string) error {
	k := key(fqdn)
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[k]
	out := list[:0]
	for _, e := range list {
		if e.value != value {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		delete(s.m, k)
	} else {
		s.m[k] = out
	}
	return nil
}

// Get returns the live (unexpired) values for fqdn.
func (s *MemStore) Get(fqdn string) ([]string, error) {
	k := key(fqdn)
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.m[k]
	out := make([]string, 0, len(list))
	for _, e := range list {
		if e.expires.After(now) {
			out = append(out, e.value)
		}
	}
	return out, nil
}

// Snapshot returns all live entries keyed by FQDN — for /debug/txt.
func (s *MemStore) Snapshot() (map[string][]string, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]string, len(s.m))
	for k, list := range s.m {
		vals := make([]string, 0, len(list))
		for _, e := range list {
			if e.expires.After(now) {
				vals = append(vals, e.value)
			}
		}
		if len(vals) > 0 {
			out[k] = vals
		}
	}
	return out, nil
}

// GC removes expired entries; call on a timer. Only MemStore needs it — Redis expires natively.
func (s *MemStore) GC() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, list := range s.m {
		out := list[:0]
		for _, e := range list {
			if e.expires.After(now) {
				out = append(out, e)
			}
		}
		if len(out) == 0 {
			delete(s.m, k)
		} else {
			s.m[k] = out
		}
	}
}

// RunGC ticks GC every interval until stop is closed.
func (s *MemStore) RunGC(stop <-chan struct{}, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.GC()
		}
	}
}
