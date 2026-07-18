package ipname

import "testing"

func TestParseV4(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"192-168-1-50", "192.168.1.50", true},
		{"0-0-0-0", "0.0.0.0", true},
		{"255-255-255-255", "255.255.255.255", true},
		{"10-0-0-1", "10.0.0.1", true},

		{"192-168-1", "", false},        // too few octets
		{"192-168-1-2-3", "", false},    // too many
		{"192-168-1-256", "", false},    // out of range
		{"192.168.1.50", "", false},     // dots, not dashes
		{"abc-def-ghi-jkl", "", false},  // non-numeric
		{"", "", false},                 // empty
		{"--", "", false},               // garbage
		{"192-168--1-50", "", false},    // empty octet
	}
	for _, c := range cases {
		got, ok := ParseV4(c.in)
		if ok != c.ok {
			t.Errorf("ParseV4(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got.String() != c.want {
			t.Errorf("ParseV4(%q) = %s, want %s", c.in, got.String(), c.want)
		}
	}
}

func TestParseV6(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2001-db8--1", "2001:db8::1", true},
		{"--1", "::1", true},
		{"fe80--1", "fe80::1", true},
		{"2001-0db8-85a3-0000-0000-8a2e-0370-7334", "2001:db8:85a3::8a2e:370:7334", true},

		{"2001-db8", "", false},       // too few groups, no "::"
		{"192-168-1-50", "", false},   // v4, not v6
		{"2001---db8--1", "", false},  // ::: is invalid
		{"", "", false},
		{"2001-db8-zz", "", false},
		// 4in6 is rejected — keep AAAA and A worlds disjoint.
		{"--ffff-c0a8-101", "", false},
	}
	for _, c := range cases {
		got, ok := ParseV6(c.in)
		if ok != c.ok {
			t.Errorf("ParseV6(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got.String() != c.want {
			t.Errorf("ParseV6(%q) = %s, want %s", c.in, got.String(), c.want)
		}
	}
}
