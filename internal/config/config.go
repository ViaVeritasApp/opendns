// Package config loads runtime settings from environment variables.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

type Config struct {
	Zone       string                // FQDN with trailing dot, lower-case, e.g. "lan.curifyapp.com."
	NS         []string              // NS hostnames with trailing dot
	Glue       map[string]netip.Addr // hostname -> A glue (IPv4 only for v1)
	SOAMbox    string                // RNAME with trailing dot
	Refresh    uint32
	Retry      uint32
	Expire     uint32
	MinTTL     uint32
	DNSBind    string
	AdminBind  string
	AdminToken string // bearer token for /acme-challenge and /debug/txt
	// AllowNoAuth lets the admin API run tokenless; else an empty AdminToken is a startup error (fail closed).
	AllowNoAuth bool

	// Shared TXT store: RedisAddr set keeps ACME challenge records in Redis so
	// multiple instances behind the same NS delegation serve them; empty = in-process.
	RedisAddr      string
	RedisPassword  string
	RedisDB        int
	RedisKeyPrefix string // key namespace; defaults to "opendns:txt:"
}

func Load() (Config, error) {
	c := Config{
		Zone:       getenv("OPENDNS_ZONE", "lan.curifyapp.com."),
		SOAMbox:    getenv("OPENDNS_SOA_MBOX", "hostmaster.curifyapp.com."),
		DNSBind:    getenv("OPENDNS_DNS_BIND", ":53"),
		AdminBind:  getenv("OPENDNS_ADMIN_BIND", "127.0.0.1:8080"),
		AdminToken: os.Getenv("OPENDNS_ADMIN_TOKEN"),

		AllowNoAuth: boolenv("OPENDNS_ADMIN_ALLOW_NO_AUTH"),

		RedisAddr:      os.Getenv("OPENDNS_REDIS_ADDR"),
		RedisPassword:  os.Getenv("OPENDNS_REDIS_PASSWORD"),
		RedisKeyPrefix: getenv("OPENDNS_REDIS_PREFIX", "opendns:txt:"),
	}

	// Fail closed: the admin API can mint DNS-01 challenges (cert-issuance risk),
	// so require a token unless the operator explicitly opts out.
	if c.AdminToken == "" && !c.AllowNoAuth {
		return c, fmt.Errorf("OPENDNS_ADMIN_TOKEN is required (or set OPENDNS_ADMIN_ALLOW_NO_AUTH=true to run the admin API unauthenticated on a trusted network)")
	}

	var err error
	if c.RedisDB, err = intenv("OPENDNS_REDIS_DB", 0); err != nil {
		return c, err
	}
	if c.Refresh, err = u32env("OPENDNS_SOA_REFRESH", 3600); err != nil {
		return c, err
	}
	if c.Retry, err = u32env("OPENDNS_SOA_RETRY", 600); err != nil {
		return c, err
	}
	if c.Expire, err = u32env("OPENDNS_SOA_EXPIRE", 1_209_600); err != nil {
		return c, err
	}
	if c.MinTTL, err = u32env("OPENDNS_SOA_MINTTL", 60); err != nil {
		return c, err
	}

	c.Zone = dns.Fqdn(strings.ToLower(c.Zone))
	c.SOAMbox = dns.Fqdn(strings.ToLower(c.SOAMbox))

	nsRaw := os.Getenv("OPENDNS_NS")
	if nsRaw == "" {
		return c, fmt.Errorf("OPENDNS_NS is required (comma-separated NS hostnames)")
	}
	for _, n := range strings.Split(nsRaw, ",") {
		n = dns.Fqdn(strings.ToLower(strings.TrimSpace(n)))
		if n == "." || n == "" {
			return c, fmt.Errorf("OPENDNS_NS: empty hostname")
		}
		c.NS = append(c.NS, n)
	}

	glueRaw := os.Getenv("OPENDNS_GLUE")
	if glueRaw == "" {
		return c, fmt.Errorf("OPENDNS_GLUE is required (host=ip,host=ip)")
	}
	c.Glue = map[string]netip.Addr{}
	for _, pair := range strings.Split(glueRaw, ",") {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			return c, fmt.Errorf("OPENDNS_GLUE: malformed pair %q (want host=ip)", pair)
		}
		host := dns.Fqdn(strings.ToLower(strings.TrimSpace(pair[:eq])))
		ipStr := strings.TrimSpace(pair[eq+1:])
		addr, perr := netip.ParseAddr(ipStr)
		if perr != nil || !addr.Is4() {
			return c, fmt.Errorf("OPENDNS_GLUE: %q is not a valid IPv4 address", ipStr)
		}
		c.Glue[host] = addr
	}

	// In-zone NS hostnames must have glue or the parent delegation loops; out-of-zone NS need none.
	for _, n := range c.NS {
		if strings.HasSuffix(n, c.Zone) {
			if _, ok := c.Glue[n]; !ok {
				return c, fmt.Errorf("OPENDNS_GLUE missing entry for in-zone NS %q", n)
			}
		}
	}

	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func u32env(k string, def uint32) (uint32, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return uint32(n), nil
}

func boolenv(k string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	return v == "1" || v == "true" || v == "yes"
}

func intenv(k string, def int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return n, nil
}
