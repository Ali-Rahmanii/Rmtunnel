# Getting through filtering: how this works, and why it's built this way

This is the part of the project aimed specifically at Iran's current
filtering, and at the question "if one way of carrying the tunnel starts
getting blocked, how does it recover without you sitting at a terminal
editing config files at 2am."

## The five disguises

Configured per entry under `[[disguise]]` in both `server.toml` and
`client.toml` — see `examples/`.

| type | what it looks like on the wire | cost |
|---|---|---|
| `plain` | our own 5-byte header, then our handshake, in the clear | cheapest CPU-wise, easiest to fingerprint |
| `noise` | uniform random bytes from the first byte on (Noise NNpsk0, keyed from your token — see `noise.go`) | small crypto overhead, no plaintext signature |
| `wss` | an ordinary TLS handshake + WebSocket upgrade — looks like HTTPS to anything that isn't specifically probing your path | TLS overhead, most code, strongest disguise |
| `kcp` | reliable UDP with always-on FEC, encrypted with a key derived from your token — not primarily a disguise, but a UDP flow doesn't get the same header-signature scrutiny a TCP one does, and it buys the lowest steady latency of the five for a lossy link | UDP is more commonly rate-limited/blocked outright on some networks than TCP:443 is |
| `quic` | an ordinary TLS 1.3 handshake over UDP — looks like HTTP/3 to anything not specifically probing it, the same idea as `wss` but for UDP-preferring paths | same ClientHello-fingerprint caveat as `wss` below, self-tuning congestion control (no manual window knobs) |

None of these hide *that* two hosts are talking a lot to each other, or make
a determined, targeted investigation impossible. What they defeat is the
cheap, automated stuff: signature matching on a fixed protocol header
(`plain`'s weakness), and — for `wss`/`quic` — a plain "what's listening on
this port" probe, which gets a normal-looking website (or HTTP/3 endpoint)
instead of anything tunnel-shaped.

**What `wss` does not do**: present a TLS ClientHello that fingerprints as a
real browser's. Go's `crypto/tls` has its own recognizable fingerprint
(cipher suite order, extension list) that differs from Chrome's or Firefox's
at the byte level — a censor doing JA3/JA4 fingerprinting rather than just
looking at the SNI can tell. Matching a real browser's fingerprint needs a
library like [utls](https://github.com/refraction-networking/utls), which
BackPack depends on for exactly this. It was left out here to keep the
dependency list and the code both small; it is the natural next step if
`wss` alone stops being enough.

**What actually matters most for `wss`**: a real certificate for a domain
you control (`cert_file`/`key_file` in the config), not the auto-generated
self-signed one. A self-signed cert is still functional and still gets you
past simple SNI-allowlist filtering, but it is itself a distinguishing mark
under closer inspection.

## Why there's no "servers agree to switch" protocol

The obvious design for "if a transport is having trouble, switch" is a
coordination message: one end notices, tells the other, both change over
together. This project deliberately does *not* do that, for a concrete
reason: the message asking to switch would have to cross the same network
path that is already having trouble — possibly the same connection that is
about to be reset. Coordination channels are themselves one more thing to
filter, and now recovery depends on one working.

Instead:

- **The server listens on every enabled disguise, all the time.**
  `server.go`'s `Run` starts one listener per `[[disguise]]` entry and keeps
  them all up for the life of the process. There is no "currently active
  profile" to switch on the server side — whichever one the client's next
  connection attempt uses, that listener is already live.
- **The client tracks each profile's own recent luck** (`profiles.go`) and
  walks down its preference list, skipping anything still in a cool-down
  from a recent failure. A profile that dies *fast* (under 20 seconds —
  `shortLivedThreshold`) is treated as more likely to be active interference
  than an ordinary blip, and gets a harder, longer cool-down. A profile
  whose control channel measures consistently high round-trip time (over 4
  seconds, three heartbeats running — `degradedRTTMs`/`degradedStreak`) is
  treated as unhealthy too, even without a hard disconnect: sustained bad
  latency on a route that used to be fine is itself a sign to try something
  else, not just "reconnect and hope."
- **Recovery is automatic and needs no signal from the other end**: a
  cooled-down profile is retried again once its cool-down expires, and if it
  works, everything is already flowing over it — there was never a moment
  where the two ends disagreed about which profile was "current."

This was tested directly (kill the path a live connection is using, watch
the client fall through plain → noise → wss on its own and climb back to the
most-preferred one the moment it's viable again — see the project's test
notes) rather than just designed on paper.

## What to actually configure for an Iran deployment

1. Enable all the disguises you plan to use. Order them `wss`, `noise`,
   `plain` in the **client's** config — server-side order doesn't matter, it
   listens on all of them regardless. Add `kcp` and/or `quic` if UDP gets
   through on your path too — same automatic-failover machinery, just more
   profiles to fall through.
2. Put `wss` on port 443. It is the least likely port to be blocked
   wholesale (too much collateral damage to legitimate HTTPS traffic), and a
   TLS-looking connection to 443 is the least suspicious thing this project
   can produce.
3. Get a real certificate for a domain you control and point `cert_file`/
   `key_file` at it, rather than relying on the auto-generated self-signed
   one.
4. Run `rmtunnel bench` (see `docs/TUNING.md`) to size the buffers for the
   actual measured RTT and bandwidth of the path once it's up — a link this
   route often has meaningfully higher RTT than a same-country connection,
   and undersized buffers cap throughput well below what the link can
   actually carry regardless of which disguise is in use.
5. Watch the logs (`journalctl -u rmtunnel-client -f` under the systemd
   unit). A profile that keeps cooling down tells you something concrete —
   which disguise is actually having trouble on your specific path — that
   guessing never would.
