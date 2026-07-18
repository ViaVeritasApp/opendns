package txtstore

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// get is a test helper that fails on error and returns the values.
func get(t *testing.T, s *MemStore, fqdn string) []string {
	t.Helper()
	v, err := s.Get(fqdn)
	if err != nil {
		t.Fatalf("Get(%q): %v", fqdn, err)
	}
	return v
}

func TestSetGetDelete(t *testing.T) {
	s := NewMem()
	_ = s.Set("_acme-challenge.x.test.", "a", time.Minute)
	_ = s.Set("_acme-challenge.x.test.", "b", time.Minute)

	got := get(t, s, "_acme-challenge.x.test.")
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("got %v, want [a b]", got)
	}

	_ = s.Delete("_acme-challenge.x.test.", "a")
	if g := get(t, s, "_acme-challenge.x.test."); !reflect.DeepEqual(g, []string{"b"}) {
		t.Fatalf("after delete got %v, want [b]", g)
	}

	_ = s.Delete("_acme-challenge.x.test.", "b")
	if g := get(t, s, "_acme-challenge.x.test."); len(g) != 0 {
		t.Fatalf("after delete got %v, want empty", g)
	}
}

func TestNormalisation(t *testing.T) {
	s := NewMem()
	_ = s.Set("_ACME-Challenge.X.Test", "v", time.Minute) // no trailing dot, mixed case
	if g := get(t, s, "_acme-challenge.x.test."); !reflect.DeepEqual(g, []string{"v"}) {
		t.Fatalf("normalisation failed: %v", g)
	}
}

func TestSetIsIdempotent(t *testing.T) {
	s := NewMem()
	_ = s.Set("x.", "v", time.Minute)
	_ = s.Set("x.", "v", time.Minute)
	if g := get(t, s, "x."); !reflect.DeepEqual(g, []string{"v"}) {
		t.Fatalf("re-set should not duplicate, got %v", g)
	}
}

func TestExpiry(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	s := newMemWithClock(func() time.Time { return clock })

	_ = s.Set("x.", "v", 10*time.Second)
	if g := get(t, s, "x."); !reflect.DeepEqual(g, []string{"v"}) {
		t.Fatalf("pre-expiry got %v", g)
	}

	clock = clock.Add(11 * time.Second)
	if g := get(t, s, "x."); len(g) != 0 {
		t.Fatalf("post-expiry got %v, want empty", g)
	}

	// GC should reclaim the empty slot.
	s.GC()
	if _, ok := s.m["x."]; ok {
		t.Fatalf("GC did not remove empty entry")
	}
}

func TestSnapshot(t *testing.T) {
	s := NewMem()
	_ = s.Set("a.", "1", time.Minute)
	_ = s.Set("a.", "2", time.Minute)
	_ = s.Set("b.", "3", time.Minute)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	sort.Strings(snap["a."])
	if !reflect.DeepEqual(snap["a."], []string{"1", "2"}) {
		t.Fatalf("snap[a]=%v", snap["a."])
	}
	if !reflect.DeepEqual(snap["b."], []string{"3"}) {
		t.Fatalf("snap[b]=%v", snap["b."])
	}
}
