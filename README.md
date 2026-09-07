# rmtunnel

A small, self-contained reverse-tunnel core: **TCP** and **TCPMux**
transports, three ways to disguise the connection (`plain` / `noise` /
`wss`), automatic failover between them, and a built-in benchmark to size
the config for your actual hardware and link. No web panel, no telegram bot
— the goal isn't to out-feature a mature project like
[BackPack](https://github.com/AminMGMT/BackPack); it's that every knob
affecting performance or how the tunnel gets through filtering is a field in
[config.go](config.go) you can find in ten seconds, and a code path you can
read start to finish.

## Architecture

```
  user ──▶  server (Iran)  ══ tunnel (plain/noise/wss) ══▶  client (Kharej)  ──▶  real backend
            opens the public port                            dials the server        (target in server's config)
```

The server listens on `listen_addr`, and on every port under `[[ports]]`.
The client dials *out* to the server — the Kharej side never needs an open
inbound port. Each forwarded port's `target` is set on the **server**, and
the **client** is the one that dials it — so the target can be anything the
client machine can reach, not just its own localhost.

### Two transport modes

| mode | how it works | when |
|---|---|---|
| `tcp` | Each tunnel connection is used **once**: a handful of pre-authenticated connections sit idle (`min_idle`), one is claimed the instant a real user connects, and a replacement is dialed to refill the pool. The tunnel-side handshake latency never sits on a real user's connection. | simplest, lowest CPU overhead |
| `tcpmux` | A few long-lived sessions ([smux](https://github.com/xtaci/smux) v2) each carry many concurrent flows as multiplexed streams — far fewer real sockets under high concurrent load. | many simultaneous users/connections |

`mode` must match on both ends — there is no negotiation on the wire for it.

### Three disguises, tried in order, switched automatically

```toml
[[disguise]]
type = "wss"     # TLS + WebSocket — looks like an ordinary HTTPS site
...
[[disguise]]
type = "noise"   # encrypted, no fixed protocol header to fingerprint
...
[[disguise]]
type = "plain"   # raw TCP, cheapest, most easily fingerprinted
```

The **server listens on every enabled one, all the time**. The **client**
tries them in the order they're listed, skipping any still cooling down from
a recent failure, and automatically falls back — no config edit, no
restart — if the one it's using starts failing or its RTT goes bad. Full
writeup, including why this beats an explicit "servers agree to switch"
handshake: **[docs/CENSORSHIP.md](docs/CENSORSHIP.md)**.

## Protocol (summary — full detail in [protocol.go](protocol.go))

1. Every raw connection opens with a 5-byte header: magic `RMT1` + one role
   byte (`control` or `pool`) — under `noise`, this whole exchange happens
   *inside* an already-encrypted channel, so there's nothing to see from
   outside even for the header.
2. The server sends a random 16-byte challenge; the peer answers with
   `HMAC-SHA256(token, challenge)`. **The token itself never crosses the
   wire.**
3. The control channel, once per "generation," issues an 8-byte epoch nonce
   that every pool connection must present — an orphaned connection from a
   previous generation can't join a new pool.
4. The control channel is fully duplex: the server sends heartbeats and
   `NEED_CONN` requests, the client sends its own heartbeat back — which
   doubles as an RTT probe (see `docs/CENSORSHIP.md`'s health section).

## Running it

The easy way — an interactive menu (colored, in Persian) that walks you
through setting up either side, tuning the OS, benchmarking, checking
service status, updating, or uninstalling:

```bash
sudo rmtunnel
```

or directly, once you have a config (what the menu's wizards produce):

```bash
./rmtunnel server server.toml     # on the Iran box
./rmtunnel client client.toml     # on the Kharej box
```

Commented example configs, if you'd rather write one by hand:
[examples/server.toml](examples/server.toml),
[examples/client.toml](examples/client.toml).

### The menu

```
╔══════════════════════════════════════════════════╗
║                     RM Tunnel                     ║
║                    نسخه 0.1.0                     ║
║      https://github.com/Ali-Rahmanii/rmtunnel     ║
║              توسعه‌دهنده: Ali Rahmani              ║
╚══════════════════════════════════════════════════╝

منوی اصلی
  1)  ساخت تانل ایران (سرور)
  2)  ساخت تانل خارج (کلاینت)
  3)  تیون سرور (بهینه‌سازی سیستم‌عامل)
  4)  بنچمارک سرعت و سخت‌افزار
  5)  وضعیت سرویس‌ها
  6)  آپدیت اسکریپت
  7)  حذف نصب
  0)  خروج
```

Options 1/2 are wizards that ask for a token, mode, which disguises to
enable, and (server side) which ports to forward — then write the config and
offer to install it as a systemd service on the spot. Option 3 applies the
sysctl tuning from `docs/TUNING.md` (BBR, socket buffer ceilings). Option 6
checks this repo's GitHub Releases and replaces the running binary in place.

## Sizing the config for your hardware and link

```bash
# on one box:
rmtunnel bench server 0.0.0.0:9999 some-temp-token
# on the other:
rmtunnel bench client <that-box-ip>:9999 some-temp-token
```

Measures real RTT and throughput between the two boxes, reads local CPU/RAM,
and prints a recommended tier — سبک/متوسط/سنگین/وحشتناک (light / medium /
heavy / insane) — plus the exact config block to paste in. See
[docs/TUNING.md](docs/TUNING.md) for what each of those knobs actually does.

## Installing on a server

```bash
curl -fsSL https://raw.githubusercontent.com/Ali-Rahmanii/rmtunnel/main/install.sh | sudo bash
```

Downloads the right binary for the box's architecture (amd64/arm64) from
this repo's GitHub Releases, and drops example configs in `/etc/rmtunnel/`.
systemd unit files for running it as a service: [systemd/](systemd/).

## Building from source

```bash
go build -o rmtunnel .                                                # native
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o rmtunnel-linux .    # cross-compile
```

Nothing in this project reaches for an OS-specific syscall except two
explicitly opt-in, build-tagged extras (`mss_linux.go`, `reuseport_linux.go`
— both no-ops on any other OS). Everything else builds and runs natively on
Windows for local development and testing, and cross-compiles to Linux
without any special setup.

## What's tested

- All three disguises (`plain`, `noise`, `wss`), end-to-end, both transport
  modes, against a real HTTP backend
- Concurrent load (20 simultaneous requests) on every disguise/mode
  combination
- Automatic profile failover: killed the active path mid-session, watched
  the client fall through the remaining profiles on its own and climb back
  to the most-preferred one the moment it recovered — with zero manual
  intervention
- Client disconnect/reconnect, control-channel liveness (RTT/heartbeat) over
  a sustained connection
- `rmtunnel bench` end-to-end (and two real desync bugs found and fixed
  during that testing — see `bench.go`'s comments)
- Cross-compilation to linux/amd64, linux/arm64, plus native Windows

## What isn't here (on purpose — see docs/TUNING.md and docs/CENSORSHIP.md)

- A TLS ClientHello that fingerprints as a real browser's (needs a uTLS-style
  library)
- UDP forwarding (this project is TCP-only)
- Per-connection bandwidth/rate limiting
- A metrics dashboard beyond the periodic stats log line
