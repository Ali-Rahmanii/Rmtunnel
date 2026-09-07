# rmtunnel

A small, self-contained reverse-tunnel core: **TCP and UDP** forwarding over
**TCP/TCPMux** transports, three ways to disguise the connection (`plain` /
`noise` / `wss`), automatic failover between them, multi-tunnel management,
and a built-in benchmark that sizes the config to your actual hardware and
link — and feeds straight into the setup wizard, so you don't have to
hand-copy numbers. No web panel, no telegram bot — the goal isn't to
out-feature a mature project like
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
client machine can reach, not just its own localhost. Setting `udp = true`
on a `[[ports]]` entry also relays datagrams on that same port through the
tunnel (WireGuard, game servers, anything UDP) — see [udp.go](udp.go).

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

The easy way — an interactive, colored menu that walks you through setting
up either side, tuning the OS, benchmarking, checking service status,
updating, or uninstalling:

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
║                       v0.1.0                      ║
║      https://github.com/Ali-Rahmanii/rmtunnel     ║
║                  by Ali Rahmani                   ║
╚══════════════════════════════════════════════════╝

Main Menu
  1)  Build Iran tunnel (server)
  2)  Build Kharej tunnel (client)
  3)  Manage tunnels
  4)  Tune server (OS optimization)
  5)  Speed & hardware benchmark
  6)  Update script
  7)  Uninstall
  0)  Exit
```

Options 1/2 are wizards that ask for a name (a box can run more than one
tunnel — see below), a token, mode, which disguises to enable, and (server
side) which ports to forward — accepting `1232`, `1232:2323`, or the
explicit `1232=host:2323`, comma-separated for several at once, plus one
question about also relaying UDP on them. Buffer sizing is either a live
benchmark against the other box run right there in the wizard, numbers you
already know entered by hand, or a named tier — see "Sizing the config"
below. The wizard then writes the config and offers to install it as a
systemd service on the spot.

Option 3, **Manage tunnels**, lists every tunnel configured on the box and
lets you edit (token, ports, disguises, or performance tier — each flagged
if the peer needs the same change), start/stop/restart, tail logs, or
delete it. A box can run several tunnels at once (each is its own named
systemd service instance, `rmtunnel-<role>@<name>`) — a Iran box forwarding
several unrelated services, say, or one box running both a server tunnel
for one purpose and a client tunnel for another.

Option 4 applies the sysctl tuning from `docs/TUNING.md` (BBR, socket buffer
ceilings). Option 6 checks this repo's GitHub Releases and replaces the
running binary in place.

## Sizing the config for your hardware and link

```bash
# on one box:
rmtunnel bench server 0.0.0.0:9999 some-temp-token
# on the other:
rmtunnel bench client <that-box-ip>:9999 some-temp-token
```

Measures real RTT and throughput between the two boxes, reads local CPU/RAM,
and prints a recommended tier (light / medium / heavy / insane) — plus the
exact config block to paste in, with `recv_buf`/`send_buf`/`mux_stream_buffer`
floored at the link's actual bandwidth-delay product. **This is the single
biggest factor in real throughput** — a buffer smaller than bandwidth×RTT
caps a connection's speed no matter how fast the link actually is,
regardless of which disguise is carrying it. The setup wizard (menu options
1/2) can run this same measurement for you inline instead of you copying
numbers over by hand. See [docs/TUNING.md](docs/TUNING.md) for what each
knob actually does.

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
- UDP forwarding end-to-end, including session reuse across multiple
  datagrams from the same source
- Concurrent load (20 simultaneous requests) on every disguise/mode
  combination
- Automatic profile failover: killed the active path mid-session, watched
  the client fall through the remaining profiles on its own and climb back
  to the most-preferred one the moment it recovered — with zero manual
  intervention
- Client disconnect/reconnect, control-channel liveness (RTT/heartbeat) over
  a sustained connection
- `rmtunnel bench` end-to-end (and two real desync bugs found and fixed
  during that testing — see `bench.go`'s comments), plus the wizard's live
  and manual buffer-sizing paths
- The interactive menu's wizards (server and client), including every port
  format, with scripted input — and a real bug this caught: input running
  out used to spin the menu forever instead of exiting, now fixed
- Cross-compilation to linux/amd64, linux/arm64, plus native Windows

## A real bug this project found in itself

Early on, the `wss` disguise was measurably far slower than its raw
benchmarked bandwidth suggested it should be. The cause: `recv_buf`/
`send_buf`/`nodelay` were being applied to every disguise's connection
*except* `wss`'s — gorilla/websocket dials its own raw TCP connection
internally, and nothing was hooking into that to tune it, so it ran at
whatever the OS default socket buffers happened to be. Fixed by supplying
`NetDialContext` on the client dialer and wrapping the server's listener so
every accepted connection is tuned before TLS or the WebSocket upgrade ever
touches it — see the comments in `wss.go` and `bench.go`'s `tunedTier`.

## What isn't here (on purpose — see docs/TUNING.md and docs/CENSORSHIP.md)

- A TLS ClientHello that fingerprints as a real browser's (needs a uTLS-style
  library)
- Per-connection bandwidth/rate limiting
- A metrics dashboard beyond the periodic stats log line
- Pushing a config change to the peer automatically (the editor tells you
  when a change needs the same edit on the other side — it doesn't reach
  across and make it, on purpose; see docs/CENSORSHIP.md)
