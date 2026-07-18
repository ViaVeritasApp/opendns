package txtstore

import (
	"os"
	"reflect"
	"sort"
	"testing"
	"time"
)

// These exercise real Redis (sorted-set scores, native expiry, SCAN). Set
// OPENDNS_TEST_REDIS_ADDR (e.g. "127.0.0.1:6379") to run; without it they skip so CI stays green.
func newTestRedis(t *testing.T) *RedisStore {
	t.Helper()
	addr := os.Getenv("OPENDNS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set OPENDNS_TEST_REDIS_ADDR to run RedisStore tests")
	}
	// Unique prefix per test so parallel runs and reruns don't collide.
	s := NewRedis(RedisConfig{Addr: addr, KeyPrefix: "opendns:test:" + t.Name() + ":"})
	if err := s.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() {
		snap, _ := s.Snapshot()
		for fqdn, vals := range snap {
			for _, v := range vals {
				_ = s.Delete(fqdn, v)
			}
		}
		_ = s.Close()
	})
	return s
}

func rget(t *testing.T, s *RedisStore, fqdn string) []string {
	t.Helper()
	v, err := s.Get(fqdn)
	if err != nil {
		t.Fatalf("Get(%q): %v", fqdn, err)
	}
	sort.Strings(v)
	return v
}

func TestRedisSetGetDelete(t *testing.T) {
	s := newTestRedis(t)
	if err := s.Set("_acme-challenge.x.test.", "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("_acme-challenge.x.test.", "b", time.Minute); err != nil {
		t.Fatal(err)
	}
	if g := rget(t, s, "_acme-challenge.x.test."); !reflect.DeepEqual(g, []string{"a", "b"}) {
		t.Fatalf("got %v, want [a b]", g)
	}

	if err := s.Delete("_acme-challenge.x.test.", "a"); err != nil {
		t.Fatal(err)
	}
	if g := rget(t, s, "_acme-challenge.x.test."); !reflect.DeepEqual(g, []string{"b"}) {
		t.Fatalf("after delete got %v, want [b]", g)
	}
}

func TestRedisNormalisation(t *testing.T) {
	s := newTestRedis(t)
	if err := s.Set("_ACME-Challenge.X.Test", "v", time.Minute); err != nil { // no trailing dot, mixed case
		t.Fatal(err)
	}
	if g := rget(t, s, "_acme-challenge.x.test."); !reflect.DeepEqual(g, []string{"v"}) {
		t.Fatalf("normalisation failed: %v", g)
	}
}

func TestRedisSetIsIdempotent(t *testing.T) {
	s := newTestRedis(t)
	_ = s.Set("x.test.", "v", time.Minute)
	_ = s.Set("x.test.", "v", time.Minute)
	if g := rget(t, s, "x.test."); !reflect.DeepEqual(g, []string{"v"}) {
		t.Fatalf("re-set should not duplicate, got %v", g)
	}
}

func TestRedisExpiry(t *testing.T) {
	s := newTestRedis(t)
	if err := s.Set("x.test.", "v", time.Second); err != nil {
		t.Fatal(err)
	}
	if g := rget(t, s, "x.test."); !reflect.DeepEqual(g, []string{"v"}) {
		t.Fatalf("pre-expiry got %v", g)
	}
	time.Sleep(1100 * time.Millisecond)
	if g := rget(t, s, "x.test."); len(g) != 0 {
		t.Fatalf("post-expiry got %v, want empty", g)
	}
}

func TestRedisSnapshot(t *testing.T) {
	s := newTestRedis(t)
	_ = s.Set("a.test.", "1", time.Minute)
	_ = s.Set("a.test.", "2", time.Minute)
	_ = s.Set("b.test.", "3", time.Minute)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	sort.Strings(snap["a.test."])
	if !reflect.DeepEqual(snap["a.test."], []string{"1", "2"}) {
		t.Fatalf("snap[a]=%v", snap["a.test."])
	}
	if !reflect.DeepEqual(snap["b.test."], []string{"3"}) {
		t.Fatalf("snap[b]=%v", snap["b.test."])
	}
}
