# opendns

Tiny authoritative DNS server for the ViaVeritas bridge's `.lan.viaveritas.app`
zone. Decodes a private LAN IP out of the hostname (Plex-style) and serves
ACME DNS-01 challenge TXT records so the backend can mint trusted Let's
Encrypt certificates for every clinic bridge.

## What it answers

| Query | Example | Response |
|---|---|---|
| **A** for IPv4-encoded label | `dig 192-168-1-50.bridge123.lan.viaveritas.app A` | `192.168.1.50` |
| **AAAA** for IPv6-encoded label | `dig 2001-db8--1.bridge123.lan.viaveritas.app AAAA` | `2001:db8::1` |
| **TXT** for `_acme-challenge.*` | `dig _acme-challenge.bridge123.lan.viaveritas.app TXT` | values from in-memory store |
| **SOA / NS** for the zone apex | `dig lan.viaveritas.app SOA` | static from config |
| **A** for configured NS hostnames (glue) | `dig ns1.lan.viaveritas.app A` | static from config |
| anything else in-zone | | empty NOERROR + SOA in authority |
| anything out-of-zone | | REFUSED |

Label encoding follows the [sslip.io](https://sslip.io) / [nip.io](https://nip.io) convention:
dashes separate v4 octets or v6 groups; `--` is the `::` shorthand for v6.

## Configuration

End-to-end recipe for the Plex-style HTTPS scheme: `opendns` (this repo) +
Cloudflare delegation + MikroTik NAT + bridge code changes + NestJS API
changes + CRM front-end change. Each phase is independently testable; do
them in order and verify before moving on.

**Topology recap.**

```
  Internet ──► MikroTik (public IP)
                    │  UDP/TCP :53 ─DNAT─► LXC :53  (opendns)
                    │  (admin :8080 NOT forwarded, internal only)
                    │
  Clinic LAN ──► Bridge PC (e.g. 192.168.1.50)
                    │  HTTPS :5000 (bridgesrv)
                    │  HTTPS :8080 (dufs)
                    │  hostname: 192-168-1-50.<bridgeId>.lan.viaveritas.app
                    │
                    └── CRM browser (viaveritas.app over HTTPS)
                        fetches https://192-168-1-50.<bridgeId>.lan.viaveritas.app:5000
                        public DNS resolves → 192.168.1.50 → over LAN
```

---

You need:

- A public IPv4 for the DNS server (MikroTik WAN, static).
- An LXC (or VM/container) on the LAN behind MikroTik for `opendns`.
- Owner access to the Cloudflare zone `viaveritas.app`.
- A Let's Encrypt account (free; created automatically by the ACME client).
- The NestJS `api` repo and the Go `bridge` repo at writable checkouts.

Pick names now and use them everywhere:

| Symbol | Example | Use |
|---|---|---|
| `<MikroTik_IPv4>` | `203.0.113.10` | MikroTik WAN, advertised as the NS A record |
| `<LXC_IP>` | `192.168.88.10` | LXC interface inside MikroTik LAN |
| `<ZONE>` | `lan.viaveritas.app` | Delegated zone |
| `<NS_HOST>` | `ns.opendns.viaveritas.app` | Out-of-zone nameserver hostname (easier — no glue) |

---

### Phase 1 — Deploy `opendns` in the LXC

#### 1.1 Provision the LXC

Anything with a public-IP-reachable interface and ~32MB RAM. Debian/Ubuntu/Alpine all fine.

#### 1.2 Install Docker (or run the binary directly)

```sh
# Docker route
apt update && apt install -y docker.io
```

Or build the static binary on a build host and `scp` it in (no runtime deps).

#### 1.3 Run the container

```sh
docker compose up -d
```

Generate the admin token once with `openssl rand -hex 32` (or any 32+ byte
random string) and store it in a secret manager. The API will need the
exact same value as its `OPENDNS_ADMIN_TOKEN` env var (Phase 4.2).

Notes:
- `OPENDNS_GLUE` must contain the **public IP** Cloudflare will publish for the NS host. This server serves the same record back on direct lookups.
- Admin port is bound to `127.0.0.1:8080` on the LXC host — only the API (running on the same LXC or reachable over a private network) can hit it. **Do not expose 8080 publicly.**
- If the API runs on a different host, bind admin to a private interface (e.g. WireGuard) instead of `127.0.0.1`.

#### 1.5 Running multiple instances (high availability / scale)

A/AAAA/SOA/NS answers are **stateless** — every instance computes them from the
hostname label and config, so they're identical no matter which instance a query
hits. The one piece of per-instance state is the **ACME DNS-01 TXT store**: an
admin `POST /acme-challenge` only writes to the instance it lands on, but a
validator's TXT query can hit any instance. With more than one instance and the
default in-memory store, issuance fails intermittently.

To run more than one instance, give them a **shared Redis** so the TXT store is
common to all of them:

| Var | Meaning |
|---|---|
| `OPENDNS_REDIS_ADDR` | `host:port` of the shared Redis. **Unset → in-memory store (single instance).** |
| `OPENDNS_REDIS_PASSWORD` | optional AUTH password |
| `OPENDNS_REDIS_DB` | optional DB index (default `0`) |
| `OPENDNS_REDIS_PREFIX` | key namespace (default `opendns:txt:`) |

Behaviour:
- Records carry their TTL via Redis-native per-value expiry, so no garbage
  collector runs in Redis mode.
- Redis being briefly unreachable is **non-fatal**: A/AAAA/SOA keep answering and
  TXT lookups resume automatically once Redis recovers (the admin API returns
  `503` for writes while it's down). Startup logs a warning rather than crashing.
- The Redis only needs to hold short-lived challenge values — persistence can be
  disabled (`--save '' --appendonly no`). Put it on a private network reachable
  by every instance.

Each instance still binds `:53`; front them with the NS delegation listing
multiple A records, anycast, or an L4 load balancer. The admin API can target any
single instance (or be load-balanced) — the write is visible to all.

#### 1.4 Local sanity check (from the LXC itself)

```sh
dig @127.0.0.1 192-168-1-50.test.lan.viaveritas.app A +short      # → 192.168.1.50
dig @127.0.0.1 lan.viaveritas.app SOA +short
curl localhost:8080/healthz                                       # → ok (no auth)
curl localhost:8080/debug/txt                                     # → 401 (auth required)
curl -H "Authorization: Bearer $OPENDNS_ADMIN_TOKEN" \
     localhost:8080/debug/txt                                     # → {} (empty store)
```

If these fail, fix here before touching DNS or the router.

---

### Phase 2 — MikroTik NAT

Goal: WAN UDP/53 and TCP/53 → LXC :53. Do **not** forward 8080.

In the MikroTik terminal (or Winbox → IP → Firewall → NAT):

```mikrotik
# Forward UDP/53
/ip firewall nat add chain=dstnat dst-address=<MikroTik_IPv4> protocol=udp \
  dst-port=53 action=dst-nat to-addresses=10.10.10.251 to-ports=53 \
  comment="opendns udp"

# Forward TCP/53
/ip firewall nat add chain=dstnat dst-address=<MikroTik_IPv4> protocol=tcp \
  dst-port=53 action=dst-nat to-addresses=10.10.10.251 to-ports=53 \
  comment="opendns tcp"
```

If you have a default `forward`-chain `drop` rule, allow the forwarded traffic:

```mikrotik
/ip firewall filter add chain=forward dst-address=10.10.10.251 protocol=udp \
  dst-port=53 action=accept comment="opendns udp"
/ip firewall filter add chain=forward dst-address=10.10.10.251 protocol=tcp \
  dst-port=53 action=accept comment="opendns tcp"
```

Order matters: these accepts must come **above** any blanket drop in `forward`.

#### Verify from outside the LAN

From any machine on the internet:

```sh
dig @<MikroTik_IPv4> 192-168-1-50.test.lan.viaveritas.app A +short   # → 192.168.1.50
dig @<MikroTik_IPv4> lan.viaveritas.app SOA +short
```

If either times out, NAT/firewall is wrong. Don't proceed until both work.

---

### Phase 3 — Cloudflare delegation

In the Cloudflare dashboard for `viaveritas.app` → DNS → Records, add **two** records:

| Type | Name | Content | Proxy status | TTL |
|---|---|---|---|---|
| `NS` | `lan` | `ns.opendns.viaveritas.app` | **DNS only** | Auto |
| `A` | `ns.opendns` | `<MikroTik_IPv4>` | **DNS only** | Auto |

**The "DNS only" (grey cloud) is critical.** Cloudflare's orange-cloud proxy is HTTP-only and would break DNS queries.

If Cloudflare warns "this conflicts with another record under `lan.viaveritas.app`", delete any pre-existing `A`/`AAAA`/`CNAME` records at or under `lan.viaveritas.app` — once you delegate the subdomain, only NS belongs there.

#### Verify delegation propagated

```sh
# Should hop com. → viaveritas.app. (Cloudflare) → lan.viaveritas.app. (your LXC)
dig +trace 192-168-1-50.test.lan.viaveritas.app A

# From a public resolver
dig @1.1.1.1 192-168-1-50.test.lan.viaveritas.app A +short    # → 192.168.1.50
dig @8.8.8.8 192-168-1-50.test.lan.viaveritas.app A +short    # → 192.168.1.50
```

Propagation usually completes in 1–5 minutes. If `dig +trace` still shows Cloudflare answering for `lan.viaveritas.app` after 15 minutes, recheck the NS record name/content.
