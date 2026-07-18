// Package ipname decodes IPs embedded in a DNS label (e.g. "192-168-1-50" ->
// 192.168.1.50, "2001-db8--1" -> 2001:db8::1 where "--" is "::"). Same scheme as sslip.io/nip.io.
package ipname

import (
	"net/netip"
	"strings"
)

// ParseV4 returns the IPv4 in label, or ok=false if it's not a dash-separated dotted-quad.
func ParseV4(label string) (netip.Addr, bool) {
	if strings.Count(label, "-") != 3 {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(strings.ReplaceAll(label, "-", "."))
	if err != nil || !addr.Is4() {
		return netip.Addr{}, false
	}
	return addr, true
}

// ParseV6 returns the IPv6 in label, or ok=false if malformed. "--" encodes the
// "::" zero-compression shorthand; runs of three or more dashes are rejected.
func ParseV6(label string) (netip.Addr, bool) {
	if label == "" || !strings.Contains(label, "-") {
		return netip.Addr{}, false
	}
	// Reject "---" or longer runs — only "::" (encoded as "--") is allowed.
	if strings.Contains(label, "---") {
		return netip.Addr{}, false
	}
	s := strings.ReplaceAll(label, "-", ":")
	// Only one "::" is allowed per RFC 4291; ParseAddr enforces that for us.
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is6() || addr.Is4In6() {
		return netip.Addr{}, false
	}
	return addr, true
}
