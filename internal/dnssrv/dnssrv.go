// Package dnssrv implements the authoritative DNS handler.
package dnssrv

import (
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"

	"github.com/viaveritas/opendns/internal/config"
	"github.com/viaveritas/opendns/internal/ipname"
	"github.com/viaveritas/opendns/internal/txtstore"
)

// Handler answers DNS queries for a single authoritative zone.
type Handler struct {
	cfg    config.Config
	txt    txtstore.Store
	serial atomic.Uint32
}

// New returns a Handler for cfg, drawing TXT records from store.
func New(cfg config.Config, store txtstore.Store) *Handler {
	h := &Handler{cfg: cfg, txt: store}
	h.serial.Store(uint32(time.Now().Unix()))
	return h
}

// ServeDNS implements dns.Handler.
func (h *Handler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	m.Compress = true

	// Preserve EDNS0 if the requester used it.
	udpSize := uint16(512)
	if opt := req.IsEdns0(); opt != nil {
		udpSize = opt.UDPSize()
		if udpSize < 512 {
			udpSize = 512
		}
		m.SetEdns0(udpSize, opt.Do())
	}

	// We only answer well-formed standard queries.
	if len(req.Question) != 1 || req.Opcode != dns.OpcodeQuery {
		m.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(m)
		return
	}
	q := req.Question[0]
	name := strings.ToLower(dns.Fqdn(q.Name))

	// Out-of-zone -> REFUSED.
	if !inZone(name, h.cfg.Zone) {
		m.Rcode = dns.RcodeRefused
		_ = w.WriteMsg(m)
		return
	}

	h.answer(m, name, q.Qtype)

	if w.RemoteAddr().Network() == "udp" {
		m.Truncate(int(udpSize))
	}
	_ = w.WriteMsg(m)
}

func (h *Handler) answer(m *dns.Msg, name string, qtype uint16) {
	zone := h.cfg.Zone

	// 1. Configured glue: static-A queries (NS hostnames etc.) win over wildcard parsing.
	if ip, ok := h.cfg.Glue[name]; ok {
		switch qtype {
		case dns.TypeA, dns.TypeANY:
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: h.cfg.MinTTL},
				A:   ip.AsSlice(),
			})
		}
		if len(m.Answer) == 0 {
			m.Ns = append(m.Ns, h.soa())
		}
		return
	}

	// 2. Zone apex: SOA, NS, plus glue in additional section.
	if name == zone {
		h.apex(m, qtype)
		return
	}

	// 3. Wildcard subdomain.
	relative := strings.TrimSuffix(name, "."+zone) // safe: inZone confirmed suffix
	if relative == name {                          // suffix wasn't ".zone" — shouldn't happen
		m.Rcode = dns.RcodeRefused
		return
	}
	labels := dns.SplitDomainName(relative)
	if len(labels) == 0 {
		m.Ns = append(m.Ns, h.soa())
		return
	}
	leftmost := labels[0]

	switch qtype {
	case dns.TypeA, dns.TypeANY:
		if ip, ok := ipname.ParseV4(leftmost); ok {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: h.cfg.MinTTL},
				A:   ip.AsSlice(),
			})
		}
	}
	switch qtype {
	case dns.TypeAAAA, dns.TypeANY:
		if ip, ok := ipname.ParseV6(leftmost); ok {
			m.Answer = append(m.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: h.cfg.MinTTL},
				AAAA: ip.AsSlice(),
			})
		}
	}
	switch qtype {
	case dns.TypeTXT, dns.TypeANY:
		vals, err := h.txt.Get(name)
		if err != nil {
			// A backend outage must not take down the zone: log and fall through
			// to empty-NOERROR + SOA below, as if no challenge existed.
			slog.Warn("txt store lookup failed", "name", name, "err", err)
		}
		for _, v := range vals {
			m.Answer = append(m.Answer, &dns.TXT{
				Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: h.cfg.MinTTL},
				Txt: []string{v},
			})
		}
	}

	if len(m.Answer) == 0 {
		// Name conceptually exists (wildcard): NOERROR + SOA, not NXDOMAIN —
		// NXDOMAIN would poison negative caches and break ACME's retry loop.
		m.Ns = append(m.Ns, h.soa())
	}
}

func (h *Handler) apex(m *dns.Msg, qtype uint16) {
	switch qtype {
	case dns.TypeSOA, dns.TypeANY:
		m.Answer = append(m.Answer, h.soa())
	}
	switch qtype {
	case dns.TypeNS, dns.TypeANY:
		for _, ns := range h.cfg.NS {
			m.Answer = append(m.Answer, &dns.NS{
				Hdr: dns.RR_Header{Name: h.cfg.Zone, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: h.cfg.MinTTL},
				Ns:  ns,
			})
		}
		// Glue in additional for in-zone NS hostnames.
		for _, ns := range h.cfg.NS {
			if ip, ok := h.cfg.Glue[ns]; ok {
				m.Extra = append(m.Extra, &dns.A{
					Hdr: dns.RR_Header{Name: ns, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: h.cfg.MinTTL},
					A:   ip.AsSlice(),
				})
			}
		}
	}
	if len(m.Answer) == 0 {
		// Other apex type — empty NOERROR.
		m.Ns = append(m.Ns, h.soa())
	}
}

func (h *Handler) soa() *dns.SOA {
	return &dns.SOA{
		Hdr:     dns.RR_Header{Name: h.cfg.Zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: h.cfg.MinTTL},
		Ns:      h.cfg.NS[0],
		Mbox:    h.cfg.SOAMbox,
		Serial:  h.serial.Load(),
		Refresh: h.cfg.Refresh,
		Retry:   h.cfg.Retry,
		Expire:  h.cfg.Expire,
		Minttl:  h.cfg.MinTTL,
	}
}

// inZone reports whether name is at or below zone (both are FQDNs with trailing dots).
func inZone(name, zone string) bool {
	if name == zone {
		return true
	}
	return strings.HasSuffix(name, "."+zone)
}
