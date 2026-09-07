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

### Direction: reverse (default) or direct

`direction = "reverse"` is the setup above — the client dials the server.
`direction = "direct"` flips *only* who dials whom for the tunnel's control
channel and pool/mux capacity: the server dials the client instead, and the
client listens. Everything else is unchanged — the server still owns
`[[ports]]` and still exposes them to real users; the client still resolves
and dials the real backend. Use it when the server box's inbound port
doesn't get through (blocked, NAT without a forward) but its outbound does.
See `Config.Direction`'s doc comment in [config.go](config.go) and
[direct.go](direct.go) for how it's implemented — reusing every wire-level
primitive (handshake, pool, mux) the reverse path already uses, not a
second tunnel engine.

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

Options 1/2 are wizards that ask, in order: **direction** (reverse or
direct — see above), a name (a box can run more than one tunnel — see
below), a token, **transport** (TCP or TCP Mux, each with a one-line
explanation of the tradeoff), which disguises to enable (plus, on whichever
side dials out, optional **backup addresses** per disguise — tried in order
if the primary one stops working, e.g. a second IP for the same box), and
(server side) which ports to forward — accepting `1232`, `1232:2323`, or the
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
for one purpose and a client tunnel for another. The same screen also has
**Restart ALL** (every configured tunnel, one confirm), **Health check**
(root/systemd/BBR/qdisc, per-tunnel token strength, buffer values that
exceed the OS's socket-buffer ceiling, and forwarded/disguise ports that
collide between two tunnels on the same box — each with a concrete fix, not
just a red X), and **File locations** (where every config/unit/log actually
is, for when you'd rather look yourself).

Option 4 applies the sysctl tuning from `docs/TUNING.md` (BBR, socket buffer
ceilings). Option 6 checks this repo's GitHub Releases, replaces the running
binary in place, migrates any tunnel still running under a pre-multi-tunnel
install (so it shows up in "Manage tunnels" instead of running invisibly
under a name nothing looks for anymore), and restarts every configured
tunnel so it's actually running the new binary — swapping the file on disk
doesn't touch a systemd service already running the old one in memory. The
menu also checks in the background the moment it starts, so an out-of-date
install shows a warning banner on its own rather than only when you remember
to check.

## Sizing the config for your hardware and link

```bash
# on one box:
rmtunnel bench server 0.0.0.0:9999 some-temp-token
# on the other:
rmtunnel bench client <that-box-ip>:9999 some-temp-token
```

Measures real RTT and throughput between the two boxes, reads local CPU/RAM,
and prints a recommended tier (light / medium / heavy / extreme / insane) — plus the
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
- tcpmux throughput, A/B, small vs. correctly-applied buffers, through a
  local RTT-injecting proxy (~80ms) — confirms the fix below and that
  `mux_stream_buffer` scaling actually reaches the underlying socket
- `direction = "direct"` end-to-end, both transport modes, single and
  concurrent requests — the server dialing out, the client listening,
  forwarding correctly through to the real backend either way
- Backup-address rotation: a deliberately unreachable primary address,
  confirmed the client rotates to the configured backup and connects
- Cross-compilation to linux/amd64, linux/arm64, plus native Windows

## Real bugs this project found in itself

**The big one:** every generated config (the shipped examples and the
wizard's output alike) wrote the performance-tuning block *after* the
`[[disguise]]`/`[[ports]]` sections. TOML scopes a bare key to the most
recently opened table — so every tuning value silently became a stray field
of the last `[[ports]]`/`[[disguise]]` entry instead of a top-level setting,
the file loaded without any error, and the tunnel always ran on default
(small) buffers no matter which tier you picked or what `bench` recommended.
Caught by an actual A/B throughput test (a local RTT-injecting proxy, not
just reasoning about it): **~12 MB/s with the bug, ~86 MB/s after the fix**,
same link, same tier, tcpmux mode — about 7×. Fixed by reordering both
`Config`'s struct fields and the wizard's TOML-writing functions so scalars
always come first, *and* by making `LoadConfig` check
[`MetaData.Undecoded()`](https://pkg.go.dev/github.com/BurntSushi/toml#MetaData.Undecoded)
after every decode and refuse to start on any config with an orphaned key —
so this exact class of bug can't silently happen again, in this project or
in a hand-edited config.

**Also found:** `mux_frame_size = 65536` in the `heavy`/`insane` tiers —
smux encodes frame size in 16 bits, so the max is `65535`, and `65536`
(a tempting round number) made every mux session fail to open while the
control channel stayed up, which reads as "connected but nothing works"
for a confusing reason. Fixed the tiers and added a `validate()` check.

**And earlier:** the `wss` disguise never applied `recv_buf`/`send_buf`/
`nodelay` to its underlying connection at all — gorilla/websocket dials its
own raw TCP internally, and nothing hooked into that to tune it. Fixed via
`NetDialContext` on the client dialer and a tuning listener wrapper on the
server — see `wss.go` and `bench.go`'s `tunedTier`.

**Editing a tunnel from the menu could break it permanently:** `saveConfig`
re-encodes the config with `toml.NewEncoder`, and the `Duration` wrapper type
only implemented `UnmarshalText` (needed for *reading* `"5s"` back), not
`MarshalText`. The encoder's `encoding.TextMarshaler` check only looks at the
value type, not a pointer-receiver method on it, so it never found one and
fell back to encoding `Duration`'s embedded `time.Duration` field as its own
subtable (`[heartbeat]\n  Duration = "5s"`, not valid at the top level) —
which then failed to *load* on the next start, and again on the next attempt
to edit it, since `editTunnel` reads the file before it can offer to fix
anything. The only way out was deleting the tunnel and rebuilding it from
scratch. Fixed by adding a value-receiver `MarshalText` to `Duration` in
[config.go](config.go), verified with a standalone encode→decode→validate
round-trip.

**The benchmark under-reported real link speed**, most visibly on upload and
on any link with non-trivial RTT: the timed window started immediately, so
a meaningful slice of the 4-second test was TCP still climbing out of slow
start rather than moving data at the link's real steady-state rate — and
separately, the client computed upload Mbps from its own *nominal* request
duration instead of the receiving server's actually-measured elapsed time,
which skews low because the two sides' clocks start one network hop apart.
Fixed by adding an unmeasured 2-second warmup before each timed phase, and by
having the server (the side actually counting bytes for upload) report back
its own measured elapsed time instead of the client assuming its own — see
`bench.go`'s `measureDownload`/`measureUpload` and `serveBenchConn`'s
`benchUp` case.

## What isn't here (on purpose — see docs/TUNING.md and docs/CENSORSHIP.md)

- A TLS ClientHello that fingerprints as a real browser's (needs a uTLS-style
  library)
- A raw UDP or KCP/QUIC *carrier* (as opposed to forwarding UDP traffic
  *through* the existing TCP/TCPMux carrier, which `[[ports]]`'s `udp = true`
  already does) — researched against `rathole`/`frp`, deliberately deferred
  as a large, separate undertaking rather than rushed in; see docs/TUNING.md
- A full Layer-3/IP tunnel (BackPack's GRE-in-Noise mode) or multi-socket
  bandwidth bonding — `direction = "direct"` covers the TCP-level "the
  server's inbound doesn't get through" case without the much larger scope
  of a second network stack
- Per-connection bandwidth/rate limiting
- A metrics dashboard beyond the periodic stats log line
- ACME/Let's Encrypt automation, a Telegram bot, or a web panel — this stays
  a single CLI binary with one config format
- Pushing a config change to the peer automatically (the editor tells you
  when a change needs the same edit on the other side — it doesn't reach
  across and make it, on purpose; see docs/CENSORSHIP.md)
