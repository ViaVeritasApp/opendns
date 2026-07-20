# opendns

Tiny authoritative DNS server for the ViaVeritas bridge's `.lan.viaveritas.app`
zone. Decodes a private LAN IP out of the hostname and serves
ACME DNS-01 challenge TXT records so the backend can mint trusted Let's
Encrypt certificates for every clinic bridge.

## What it answers

| Query | Example | Response |
|---|---|---|
| **A** for IPv4-encoded label | `dig 192-168-1-50.bridge123.lan.viaveritas.app A` | `192.168.1.50` |
| **AAAA** for IPv6-encoded label | `dig 2001-db8--1.bridge123.lan.viaveritas.app AAAA` | `2001:db8::1` |
| **TXT** for `_acme-challenge.*` | `dig _acme-challenge.bridge123.lan.viaveritas.app TXT` | values from the in-memory / Redis store |
| **SOA / NS** for the zone apex | `dig lan.viaveritas.app SOA` | static from config |
| **A** for configured NS hostnames (glue) | `dig ns1.lan.viaveritas.app A` | static from config |
| anything else in-zone | | empty NOERROR + SOA in authority |
| anything out-of-zone | | REFUSED |

Label encoding follows the [sslip.io](https://sslip.io) / [nip.io](https://nip.io) convention:
dashes separate v4 octets or v6 groups; `--` is the `::` shorthand for v6.

**Topology recap.**

```
  Internet ──► Firewall / Router (public IP)
          │  UDP/TCP :53 ─DNAT─► opendns host :53
          │  (admin :8080 NOT forwarded, internal only)
          │
  LAN ──► Bridge PC (e.g. 192.168.1.50)
          │  HTTPS :5000 (bridgesrv)
          │  HTTPS :8080 (dufs)
          │  hostname: 192-168-1-50.<bridgeId>.lan.viaveritas.app
          │
          └── CRM browser (viaveritas.app over HTTPS)
              fetches https://192-168-1-50.<bridgeId>.lan.viaveritas.app:5000
              public DNS resolves → 192.168.1.50 → over LAN
```

## Environment variables

Configured entirely through the environment (see [config.go](internal/config/config.go)).
The shipped [docker-compose.yml](docker-compose.yml) sets the important ones; the rest have
sensible defaults.

| Variable | Required | Default | Example | What it does |
|---|---|---|---|---|
| `OPENDNS_ZONE` | yes | `lan.curifyapp.com.` | `lan.viaveritas.app.` | The authoritative zone this server answers for. Trailing dot optional (added automatically); lower-cased. |
| `OPENDNS_NS` | **yes** | — | `ns.opendns.viaveritas.app.` | Comma-separated NS hostname(s) advertised at the zone apex. Must match the `NS` record you create at your DNS provider. |
| `OPENDNS_GLUE` | **yes** | — | `ns.opendns.viaveritas.app.=203.0.113.10` | `host=ip` pairs (IPv4 only). The `A` record opendns returns for its nameserver host(s). Must equal the **public IP** your provider publishes for that host. In-zone NS hosts *must* have an entry. |
| `OPENDNS_SOA_MBOX` | no | `hostmaster.curifyapp.com.` | `hostmaster.viaveritas.app.` | Responsible-person email in DNS form (first dot = `@`, so this means `hostmaster@viaveritas.app`). **Informational only — not an IP, has no effect on routing.** |
| `OPENDNS_ADMIN_TOKEN` | **yes**¹ | — | `a1b2…` (64 hex chars) | Bearer token guarding the admin API (`POST/DELETE /acme-challenge`, `/debug/txt`). Generate with `make token`. The API repo must send this exact value. |
| `OPENDNS_ADMIN_ALLOW_NO_AUTH` | no | `false` | `true` | Lets the admin API run without a token. Only for a fully trusted/private network — the API can mint TLS certs for the zone. |
| `OPENDNS_DNS_BIND` | no | `:53` | `:5353` | Address the DNS listener (UDP+TCP) binds. Use a high port if you can't grant `NET_BIND_SERVICE`, then NAT to it. |
| `OPENDNS_ADMIN_BIND` | no | `127.0.0.1:8080` | `0.0.0.0:8080` | Address the admin HTTP API binds. In Docker, bind `0.0.0.0` inside the container and publish only to the host loopback (compose does this). |
| `OPENDNS_SOA_REFRESH` | no | `3600` | `3600` | SOA `refresh`, seconds. |
| `OPENDNS_SOA_RETRY` | no | `600` | `600` | SOA `retry`, seconds. |
| `OPENDNS_SOA_EXPIRE` | no | `1209600` | `1209600` | SOA `expire`, seconds (14 days). |
| `OPENDNS_SOA_MINTTL` | no | `60` | `60` | SOA minimum TTL — **also the TTL stamped on every A/AAAA/TXT/NS answer.** |
| `OPENDNS_REDIS_ADDR` | no | — (in-memory) | `redis:6379` | `host:port` of a shared Redis for the TXT store. **Set this only when running more than one instance** (see [Multi-instance](#multi-instance-high-availability--scale)). Unset → single-instance in-memory store. |
| `OPENDNS_REDIS_PASSWORD` | no | — | `s3cret` | Redis AUTH password, if any. |
| `OPENDNS_REDIS_DB` | no | `0` | `0` | Redis DB index. |
| `OPENDNS_REDIS_PREFIX` | no | `opendns:txt:` | `opendns:txt:` | Redis key namespace for TXT records. |

¹ Required unless `OPENDNS_ADMIN_ALLOW_NO_AUTH=true`. The server **fails to start** if the token is
missing and no-auth isn't explicitly enabled (fail closed).

---

## Deployment

1. **Prepare** — create the DNS delegation records at your provider, then forward the router's public `:53` to the opendns host.
2. **Install** — run the container with Docker.
3. **Verify** — confirm resolution works locally, from outside the LAN, and through public resolvers.

You need:

- A public IPv4 on your edge firewall/router (static).
- A host on the LAN behind it to run `opendns` (Docker, ~32 MB RAM).
- Owner access to the DNS zone `viaveritas.app` (examples use Cloudflare).

Pick names now and use them everywhere:

| Symbol | Example | Use |
|---|---|---|
| `<ROUTER_IPv4>` | `203.0.113.10` | Firewall/router WAN public IP; advertised as the NS `A` record |
| `<HOST_IP>` | `192.168.88.10` | LAN IP of the host running opendns (the NAT target) |
| `<ZONE>` | `lan.viaveritas.app` | Delegated zone |
| `<NS_HOST>` | `ns.opendns.viaveritas.app` | Out-of-zone nameserver hostname (easier — no glue at the parent) |

---

## Phase 1 — Prepare

### 1.1 DNS provider records

Do this in the dashboard for the **parent** zone `viaveritas.app`. Examples use Cloudflare;
any provider works — the record types and values are the same.

**Create these two records:**

| Type | Name | Content / Value | Proxy | TTL |
|---|---|---|---|---|
| `NS` | `lan` | `ns.opendns.viaveritas.app` | **DNS only** | Auto |
| `A` | `ns.opendns` | `<ROUTER_IPv4>` | **DNS only** | Auto |

- The **`NS`** record delegates `lan.viaveritas.app` to your server — from here on, the parent
  provider stops answering for that subdomain and hands queries to opendns.
- The **`A`** record is the public address of the nameserver host (`<NS_HOST>`). It must equal
  `<ROUTER_IPv4>` and the IP in `OPENDNS_GLUE`.
- **"DNS only" (grey cloud) is critical** on Cloudflare — the orange-cloud proxy is HTTP-only and
  breaks DNS queries. Other providers have no equivalent; just create plain records.
- If the provider warns about a conflict under `lan.viaveritas.app`, delete any pre-existing
  `A`/`AAAA`/`CNAME` there — once the subdomain is delegated, only the `NS` belongs at the parent.

**You do NOT create A / AAAA / TXT records for the delegated zone.** Everything under
`lan.viaveritas.app` is answered live by opendns, and the parent provider no longer serves that
subdomain anyway:

| Type | Name (under the zone) | Where it comes from |
|---|---|---|
| `A` / `AAAA` | `192-168-1-50.<bridgeId>.lan.viaveritas.app` | Decoded from the hostname label by opendns — no per-host record exists. |
| `TXT` | `_acme-challenge.<bridgeId>.lan.viaveritas.app` | Pushed to opendns's admin API at issuance time, served from its store. |

**Optional — `CNAME` challenge delegation.** Only if you also want opendns to answer the ACME
DNS-01 challenge for a hostname that lives in the *parent* zone (e.g. a cert for
`app.viaveritas.app`, managed directly by Cloudflare). Point its `_acme-challenge` name into the
delegated zone:

| Type | Name | Value |
|---|---|---|
| `CNAME` | `_acme-challenge.app` | `_acme-challenge.app.lan.viaveritas.app` |

Your ACME client then writes the token to opendns (via the admin API) for
`_acme-challenge.app.lan.viaveritas.app`, and the validator follows the CNAME into opendns. Skip
this entirely if every certificate name already lives under `lan.viaveritas.app`.

### 1.2 Firewall / router NAT

Goal: forward the router's WAN **UDP/53 and TCP/53** to the opendns host (`<HOST_IP>:53`). Do
**not** forward the admin port (8080) — it can mint TLS certs for the zone and must stay internal.

The concrete commands depend on your device. Below is a **MikroTik** example; adapt the equivalent
"port forward / destination NAT" feature for your own router (pfSense, OPNsense, Ubiquiti, a cloud
security group, etc.).

<details open>
<summary><b>Phase 2 — MikroTik NAT (example)</b></summary>

In the MikroTik terminal (or Winbox → IP → Firewall → NAT):

```mikrotik
# Forward UDP/53
/ip firewall nat add chain=dstnat dst-address=<ROUTER_IPv4> protocol=udp \
  dst-port=53 action=dst-nat to-addresses=<HOST_IP> to-ports=53 \
  comment="opendns udp"

# Forward TCP/53
/ip firewall nat add chain=dstnat dst-address=<ROUTER_IPv4> protocol=tcp \
  dst-port=53 action=dst-nat to-addresses=<HOST_IP> to-ports=53 \
  comment="opendns tcp"
```

If you have a default `forward`-chain `drop` rule, allow the forwarded traffic:

```mikrotik
/ip firewall filter add chain=forward dst-address=<HOST_IP> protocol=udp \
  dst-port=53 action=accept comment="opendns udp"
/ip firewall filter add chain=forward dst-address=<HOST_IP> protocol=tcp \
  dst-port=53 action=accept comment="opendns tcp"
```

Order matters: these accepts must come **above** any blanket drop in `forward`.

</details>

The verification of NAT (a `dig` from outside the LAN) is in [Phase 3](#phase-3--verify) — it needs
opendns running first.

---

## Phase 2 — Install

**Prerequisite:** Docker with the Compose plugin already installed on `<HOST_IP>`. If you don't have
it, follow [Docker's install guide](https://docs.docker.com/engine/install/) for your OS, then come
back.

### 2.1 Configure

Generate the admin token once and drop it (plus any overrides) into a `.env` file next to
[docker-compose.yml](docker-compose.yml):

```sh
make token          # → prints a 64-char hex string; or: openssl rand -hex 32
```

```dotenv
# .env
OPENDNS_ADMIN_TOKEN=<paste the make token output>
```

The API repo will need this **exact** value as its `OPENDNS_ADMIN_TOKEN`. Store it in a secret
manager. Then edit the `environment:` block in [docker-compose.yml](docker-compose.yml) for your
zone — at minimum set `OPENDNS_GLUE` to `<NS_HOST>.=<ROUTER_IPv4>`.

### 2.2 Run

```sh
docker compose up -d              # builds the image on first run, starts in the background
docker compose logs -f            # follow logs
docker compose down               # stop and remove
```

> **Run natively instead (no Docker).** The [Makefile](Makefile) builds and runs the binary
> directly — handy for local development. `make run` reads config from the environment, so export
> the required vars first (and use a high `OPENDNS_DNS_BIND` unless you run as root, since `:53` is
> privileged):
>
> ```sh
> export OPENDNS_ZONE=lan.viaveritas.app. \
>        OPENDNS_NS=ns.opendns.viaveritas.app. \
>        OPENDNS_GLUE=ns.opendns.viaveritas.app.=203.0.113.10 \
>        OPENDNS_ADMIN_TOKEN=$(make token) \
>        OPENDNS_DNS_BIND=:5353
> make run                        # build + start; `make build` produces bin/opendns
> ```

Notes:
- `OPENDNS_GLUE` must contain the **public IP** your provider publishes for the NS host. This server
  serves the same record back on direct lookups.
- The admin port is published to `127.0.0.1:8080` on the host — only the API (on the same host or
  reachable over a private network) can hit it. **Do not expose 8080 publicly.**
- If the API runs on a different host, reach the admin API over a VPN/SSH tunnel, or set
  `OPENDNS_ADMIN_BIND` to a private interface (e.g. WireGuard) instead of loopback.

### Multi-instance (high availability / scale)

A/AAAA/SOA/NS answers are **stateless** — every instance computes them from the hostname label and
config, so they're identical no matter which instance a query hits. The one piece of per-instance
state is the **ACME DNS-01 TXT store**: an admin `POST /acme-challenge` only writes to the instance
it lands on, but a validator's TXT query can hit any instance. With more than one instance and the
default in-memory store, issuance fails intermittently.

To run more than one instance, give them a **shared Redis** so the TXT store is common to all of them
(uncomment the `redis` service in [docker-compose.yml](docker-compose.yml) and set
`OPENDNS_REDIS_ADDR`):

| Var | Meaning |
|---|---|
| `OPENDNS_REDIS_ADDR` | `host:port` of the shared Redis. **Unset → in-memory store (single instance).** |
| `OPENDNS_REDIS_PASSWORD` | optional AUTH password |
| `OPENDNS_REDIS_DB` | optional DB index (default `0`) |
| `OPENDNS_REDIS_PREFIX` | key namespace (default `opendns:txt:`) |

Behaviour:
- Records carry their TTL via Redis-native per-value expiry, so no garbage collector runs in Redis mode.
- Redis being briefly unreachable is **non-fatal**: A/AAAA/SOA keep answering and TXT lookups resume
  automatically once Redis recovers (the admin API returns `503` for writes while it's down). Startup
  logs a warning rather than crashing.
- The Redis only needs to hold short-lived challenge values — persistence can be disabled
  (`--save '' --appendonly no`). Put it on a private network reachable by every instance.

Each instance still binds `:53`; front them with the NS delegation listing multiple A records,
anycast, or an L4 load balancer. The admin API can target any single instance (or be load-balanced)
— the write is visible to all.

---

## Phase 3 — Verify

Do these in order; don't proceed past a failing step.

### 3.1 Local sanity check (from the opendns host)

```sh
dig @127.0.0.1 192-168-1-50.test.lan.viaveritas.app A +short      # → 192.168.1.50
dig @127.0.0.1 lan.viaveritas.app SOA +short
curl localhost:8080/healthz                                       # → ok (no auth)
curl localhost:8080/debug/txt                                     # → 401 (auth required)
curl -H "Authorization: Bearer $OPENDNS_ADMIN_TOKEN" \
     localhost:8080/debug/txt                                     # → {} (empty store)
```

### 3.2 From outside the LAN (NAT works)

From any machine on the internet:

```sh
dig @<ROUTER_IPv4> 192-168-1-50.test.lan.viaveritas.app A +short   # → 192.168.1.50
dig @<ROUTER_IPv4> lan.viaveritas.app SOA +short
```

### 3.3 Delegation propagated (public resolvers)

```sh
# Should hop com. → viaveritas.app. (parent provider) → lan.viaveritas.app. (your host)
dig +trace 192-168-1-50.test.lan.viaveritas.app A

# From a public resolver
dig @1.1.1.1 192-168-1-50.test.lan.viaveritas.app A +short    # → 192.168.1.50
dig @8.8.8.8 192-168-1-50.test.lan.viaveritas.app A +short    # → 192.168.1.50
```

Propagation usually completes in 1–5 minutes. If `dig +trace` still shows the parent provider
answering for `lan.viaveritas.app` after 15 minutes, recheck the `NS` record name/content from
[Phase 1.1](#11-dns-provider-records).
