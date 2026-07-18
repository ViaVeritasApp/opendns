package dnssrv

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/viaveritas/opendns/internal/config"
	"github.com/viaveritas/opendns/internal/txtstore"
)

// mockWriter is a minimal dns.ResponseWriter capturing the reply; RemoteAddr reports its network.
type mockWriter struct {
	msg     *dns.Msg
	network string
}

func (w *mockWriter) WriteMsg(m *dns.Msg) error { w.msg = m; return nil }
func (w *mockWriter) Write([]byte) (int, error) { return 0, nil }
func (w *mockWriter) LocalAddr() net.Addr       { return &net.UDPAddr{} }
func (w *mockWriter) RemoteAddr() net.Addr {
	if w.network == "tcp" {
		return &net.TCPAddr{}
	}
	return &net.UDPAddr{}
}
func (w *mockWriter) TsigStatus() error   { return nil }
func (w *mockWriter) TsigTimersOnly(bool) {}
func (w *mockWriter) Hijack()             {}
func (w *mockWriter) Close() error        { return nil }

func newTestHandler(t *testing.T) (*Handler, *txtstore.MemStore) {
	t.Helper()
	store := txtstore.NewMem()
	cfg := config.Config{
		Zone:    "test.",
		NS:      []string{"ns1.test."},
		Glue:    map[string]netip.Addr{"ns1.test.": netip.MustParseAddr("127.0.0.1")},
		SOAMbox: "hostmaster.test.",
		Refresh: 3600,
		Retry:   600,
		Expire:  1_209_600,
		MinTTL:  60,
	}
	return New(cfg, store), store
}

func query(t *testing.T, h *Handler, name string, qtype uint16) *dns.Msg {
	t.Helper()
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(name), qtype)
	w := &mockWriter{}
	h.ServeDNS(w, req)
	if w.msg == nil {
		t.Fatalf("handler did not write a reply")
	}
	return w.msg
}

func TestWildcardA(t *testing.T) {
	h, _ := newTestHandler(t)
	m := query(t, h, "192-168-1-50.bridge123.test.", dns.TypeA)
	if m.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode=%v", m.Rcode)
	}
	if len(m.Answer) != 1 {
		t.Fatalf("answer count=%d", len(m.Answer))
	}
	a, ok := m.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("answer not A: %T", m.Answer[0])
	}
	if a.A.String() != "192.168.1.50" {
		t.Fatalf("A=%s, want 192.168.1.50", a.A.String())
	}
}

func TestWildcardAAAA(t *testing.T) {
	h, _ := newTestHandler(t)
	m := query(t, h, "2001-db8--1.bridge123.test.", dns.TypeAAAA)
	if m.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode=%v", m.Rcode)
	}
	if len(m.Answer) != 1 {
		t.Fatalf("answer count=%d", len(m.Answer))
	}
	aaaa, ok := m.Answer[0].(*dns.AAAA)
	if !ok {
		t.Fatalf("answer not AAAA: %T", m.Answer[0])
	}
	if aaaa.AAAA.String() != "2001:db8::1" {
		t.Fatalf("AAAA=%s", aaaa.AAAA.String())
	}
}

func TestWildcardAReturnsNoAnswerForNonIPLabel(t *testing.T) {
	// AAAA on an IPv4-shaped label (or A on a non-IP label): empty NOERROR + SOA.
	h, _ := newTestHandler(t)
	m := query(t, h, "192-168-1-50.bridge.test.", dns.TypeAAAA)
	if m.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode=%v", m.Rcode)
	}
	if len(m.Answer) != 0 {
		t.Fatalf("expected empty answer, got %d", len(m.Answer))
	}
	if len(m.Ns) != 1 {
		t.Fatalf("expected SOA in authority")
	}
	if _, ok := m.Ns[0].(*dns.SOA); !ok {
		t.Fatalf("authority not SOA: %T", m.Ns[0])
	}
}

func TestWildcardTXT(t *testing.T) {
	h, store := newTestHandler(t)
	store.Set("_acme-challenge.bridge.test.", "abc123", time.Minute)

	m := query(t, h, "_acme-challenge.bridge.test.", dns.TypeTXT)
	if len(m.Answer) != 1 {
		t.Fatalf("answer count=%d", len(m.Answer))
	}
	txt, ok := m.Answer[0].(*dns.TXT)
	if !ok || len(txt.Txt) != 1 || txt.Txt[0] != "abc123" {
		t.Fatalf("bad TXT answer: %#v", m.Answer[0])
	}

	// Empty store -> empty NOERROR + SOA.
	store.Delete("_acme-challenge.bridge.test.", "abc123")
	m = query(t, h, "_acme-challenge.bridge.test.", dns.TypeTXT)
	if len(m.Answer) != 0 {
		t.Fatalf("expected empty answer after delete, got %d", len(m.Answer))
	}
	if len(m.Ns) != 1 {
		t.Fatalf("expected SOA in authority")
	}
}

func TestApexSOA(t *testing.T) {
	h, _ := newTestHandler(t)
	m := query(t, h, "test.", dns.TypeSOA)
	if len(m.Answer) != 1 {
		t.Fatalf("answer count=%d", len(m.Answer))
	}
	if _, ok := m.Answer[0].(*dns.SOA); !ok {
		t.Fatalf("answer not SOA: %T", m.Answer[0])
	}
}

func TestApexNSWithGlue(t *testing.T) {
	h, _ := newTestHandler(t)
	m := query(t, h, "test.", dns.TypeNS)
	if len(m.Answer) != 1 {
		t.Fatalf("answer count=%d", len(m.Answer))
	}
	if _, ok := m.Answer[0].(*dns.NS); !ok {
		t.Fatalf("answer not NS: %T", m.Answer[0])
	}
	if len(m.Extra) == 0 {
		t.Fatalf("expected glue in additional")
	}
	// EDNS0 OPT (when present) lives in Extra too; check we have an A record.
	foundA := false
	for _, rr := range m.Extra {
		if a, ok := rr.(*dns.A); ok && a.A.String() == "127.0.0.1" {
			foundA = true
		}
	}
	if !foundA {
		t.Fatalf("glue A record not found in Extra")
	}
}

func TestGlueDirectQuery(t *testing.T) {
	h, _ := newTestHandler(t)
	m := query(t, h, "ns1.test.", dns.TypeA)
	if len(m.Answer) != 1 {
		t.Fatalf("answer count=%d", len(m.Answer))
	}
	a := m.Answer[0].(*dns.A)
	if a.A.String() != "127.0.0.1" {
		t.Fatalf("glue A=%s", a.A.String())
	}
}

func TestOutOfZoneRefused(t *testing.T) {
	h, _ := newTestHandler(t)
	m := query(t, h, "example.com.", dns.TypeA)
	if m.Rcode != dns.RcodeRefused {
		t.Fatalf("rcode=%v, want REFUSED", m.Rcode)
	}
}

func TestWildcardMXEmptyNoerror(t *testing.T) {
	h, _ := newTestHandler(t)
	m := query(t, h, "192-168-1-50.bridge.test.", dns.TypeMX)
	if m.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode=%v", m.Rcode)
	}
	if len(m.Answer) != 0 {
		t.Fatalf("expected empty answer, got %d", len(m.Answer))
	}
	if len(m.Ns) != 1 {
		t.Fatalf("expected SOA in authority")
	}
}

func TestCaseInsensitive(t *testing.T) {
	h, _ := newTestHandler(t)
	m := query(t, h, "192-168-1-50.BRIDGE.TEST.", dns.TypeA)
	if len(m.Answer) != 1 {
		t.Fatalf("answer count=%d", len(m.Answer))
	}
}

func TestEDNS0Echo(t *testing.T) {
	h, _ := newTestHandler(t)
	req := new(dns.Msg)
	req.SetQuestion("192-168-1-50.bridge.test.", dns.TypeA)
	req.SetEdns0(4096, true)
	w := &mockWriter{}
	h.ServeDNS(w, req)
	if opt := w.msg.IsEdns0(); opt == nil {
		t.Fatalf("response missing OPT")
	} else if opt.UDPSize() != 4096 {
		t.Fatalf("UDP size=%d, want 4096", opt.UDPSize())
	}
}
