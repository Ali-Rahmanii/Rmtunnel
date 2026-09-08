package main

import (
	"fmt"
	"time"

	"github.com/BurntSushi/toml"
)

// PortMap describes one forwarded port: the server listens on Listen (e.g.
// Iran box, publicly reachable) and, for every connection it accepts there,
// asks the client to dial Target (e.g. 127.0.0.1:8080 on the Kharej box, or
// any address the client machine can reach). UDP additionally relays
// datagrams on the same public port through the tunnel — see udp.go.
type PortMap struct {
	Listen string `toml:"listen"`
	Target string `toml:"target"`
	UDP    bool   `toml:"udp"`
}

// DisguiseConfig is one candidate way to carry the tunnel connection:
//
//   - "plain"  — raw TCP, our own handshake only. Cheapest, and the one with
//     the clearest fingerprint (a fixed 5-byte header).
//   - "noise"  — the same raw TCP, wrapped first in a Noise NNpsk0 channel
//     keyed from Token. No plaintext header at all; the wire looks like
//     uniform random bytes from the first byte on.
//   - "wss"    — a TLS + WebSocket connection to Path. Anything else the
//     listener receives (a probe, a browser, a censor's crawler) gets
//     DecoyRoot instead, so the port looks like an ordinary website that
//     happens to also speak WebSocket on one path.
//   - "kcp"    — a reliable, ordered session over raw UDP ([xtaci/kcp-go],
//     the same library paqet and BackPack both build on), encrypted with a
//     key derived from Token. Tuned for the lowest steady latency a lossy
//     link can offer, at the cost of spending extra bandwidth on parity and
//     immediate ACKs — the low-latency "gaming" carrier. Because kcp-go's
//     stream mode makes a session behave exactly like a reliable byte
//     stream (see kcpdisguise.go), this needs no protocol changes anywhere
//     else in this project — the same handshake, pool, and mux code every
//     other disguise already uses runs on top of it unchanged.
//   - "quic"   — a TLS 1.3 session over UDP, self-tuning its own congestion
//     control (no manual window knobs, unlike "kcp") — looks like ordinary
//     HTTP/3 traffic on the wire. Reuses wss's cert_file/key_file/domain/
//     insecure_skip_verify fields as-is. See quicdisguise.go.
//
// The client tries entries in the order they appear, skipping any still in
// a failure cool-down — see profiles.go. The server simply listens on every
// enabled entry at once, so whichever one the client picks is already live;
// no coordination message ever has to cross the (possibly filtered) network
// for a switch to take effect. See docs/CENSORSHIP.md for the reasoning.
type DisguiseConfig struct {
	Type    string `toml:"type"` // "plain" | "noise" | "wss" | "kcp"
	Enabled bool   `toml:"enabled"`

	ListenAddr string `toml:"listen_addr"` // server
	ServerAddr string `toml:"server_addr"` // client

	// BackupAddrs are additional addresses for this same disguise type,
	// tried in order if ServerAddr stops working — for when the box being
	// dialed has more than one public IP/domain and one of them gets
	// blocked or goes down. Meaningless on the listening side (there's
	// nothing to fail over when you're the one being dialed). See
	// profiles.go's addr()/rotateAddr().
	BackupAddrs []string `toml:"backup_addrs"`

	// wss-only
	Domain    string `toml:"domain"`    // sent as SNI / Host; also the self-signed cert's CN
	Path      string `toml:"path"`      // the one path that upgrades to the tunnel
	CertFile  string `toml:"cert_file"` // empty = generate a self-signed cert on the fly
	KeyFile   string `toml:"key_file"`
	DecoyRoot string `toml:"decoy_root"`           // empty = a small built-in placeholder page
	Insecure  bool   `toml:"insecure_skip_verify"` // client: skip cert verification (self-signed server certs)

	// kcp-only. KCPPreset, if one of "balance"/"turbo"/"aggressive"/
	// "throughput", fills every field below from kcpdisguise.go's preset
	// table (numbers studied from BackPack's own — same names, same
	// values) and the fields are ignored; "" (or "custom") means use them
	// as given instead. Both ends must agree on every one of these — none
	// of it is negotiated, see kcpdisguise.go.
	KCPPreset       string `toml:"kcp_preset"`
	KCPMTU          int    `toml:"kcp_mtu"`
	KCPInterval     int    `toml:"kcp_interval"`
	KCPResend       int    `toml:"kcp_resend"`
	KCPNoDelay      int    `toml:"kcp_nodelay"`
	KCPNoCongestion int    `toml:"kcp_nocongestion"`
	KCPSndWnd       int    `toml:"kcp_sndwnd"`
	KCPRcvWnd       int    `toml:"kcp_rcvwnd"`
	KCPAckNoDelay   bool   `toml:"kcp_acknodelay"`
	KCPDataShards   int    `toml:"kcp_datashards"` // 0 disables FEC entirely
	KCPParityShards int    `toml:"kcp_parityshards"`
}

// Config is shared by both roles; a field only one role reads is simply
// ignored by the other. One struct, one file format, one place to look.
type Config struct {
	// Role is set by the CLI flag, not the file, but kept here so the rest of
	// the program can treat it like any other setting.
	Role string `toml:"-"`

	// Direction picks who dials whom for the tunnel's control channel and
	// pool/mux capacity — independent of Role, which is unchanged and still
	// decides who owns [[ports]] (always "server") versus who resolves and
	// dials the real backend (always "client"):
	//
	//   - "reverse" (default): the box holding the real service ("client")
	//     dials out to the box exposing ports to users ("server") — the
	//     usual setup, and what to try first.
	//   - "direct": the roles of *who dials* flip. The "server" now dials
	//     out to the "client", which listens instead — use this when the
	//     server box's inbound port doesn't get through (blocked, behind
	//     NAT without a forward, etc.) but its outbound does. "server"
	//     still exposes [[ports]] to users and "client" still resolves the
	//     backend target; only the direction of the underlying connection
	//     changes. See direct.go and dialsOut() below.
	Direction string `toml:"direction"`

	// Mode picks the transport: "tcp" (one pool connection carries exactly one
	// forwarded flow, then is discarded) or "tcpmux" (a handful of long-lived
	// sessions carry many concurrent flows multiplexed over smux streams).
	// Both ends must be configured with the same Mode — there is no
	// negotiation on the wire, which is what keeps the handshake in
	// protocol.go a few lines long instead of a fallback ladder.
	Mode string `toml:"mode"`

	// Token authenticates both directions. It never crosses the wire in the
	// clear: the handshake in protocol.go proves knowledge of it with an
	// HMAC over a fresh per-connection challenge instead — and under "noise",
	// the entire connection, handshake included, is encrypted besides.
	Token string `toml:"token"`

	// --- server-only ---
	Heartbeat Duration `toml:"heartbeat"` // control-channel liveness ping

	// --- client-only ---
	RetryMin Duration `toml:"retry_min"` // first reconnect backoff step
	RetryMax Duration `toml:"retry_max"` // backoff ceiling

	// --- shared TCP tuning (see docs/TUNING.md) ---
	Nodelay     bool     `toml:"nodelay"`
	KeepAlive   Duration `toml:"keepalive"`
	RecvBuf     int      `toml:"recv_buf"`
	SendBuf     int      `toml:"send_buf"`
	DialTimeout Duration `toml:"dial_timeout"`
	BufferSize  int      `toml:"buffer_size"` // io copy buffer, both directions
	MSS         int      `toml:"mss"`         // 0 = leave the OS default (Linux only, see mss_linux.go)
	ReusePort   bool     `toml:"reuse_port"`  // Linux only, see reuseport_linux.go

	// --- pool tuning (client) ---
	// TCP mode: how many idle, already-authenticated pool connections to keep
	// standing by, so a new local connection never waits on the tunnel-side
	// TCP handshake + auth round trip — only the local dial.
	// Mux mode: same idea, but each unit is a session, not a one-shot conn.
	MinIdle   int      `toml:"min_idle"`
	MaxIdle   int      `toml:"max_idle"`
	IdleGrace Duration `toml:"idle_grace"` // must stay above MaxIdle this long before shrinking

	// --- tcpmux tuning ---
	MaxStreamsPerSession int      `toml:"max_streams_per_session"`
	MuxFrameSize         int      `toml:"mux_frame_size"`
	MuxRecvBuffer        int      `toml:"mux_recv_buffer"`
	MuxStreamBuffer      int      `toml:"mux_stream_buffer"`
	MuxKeepAlive         Duration `toml:"mux_keepalive"`

	// Disguise and Ports are array-of-tables ([[disguise]], [[ports]]) and
	// MUST stay the last fields in this struct. TOML scopes every bare
	// key to the most recently opened table — a scalar field declared (and
	// therefore encoded) after one of these would land inside that table's
	// last entry instead of at the top level and silently vanish, which is
	// exactly the bug LoadConfig's Undecoded() check now catches on the way
	// in. Keeping the struct's own field order correct is what keeps the
	// *encoder* (tunnels.go's saveConfig) from ever producing that file in
	// the first place.
	Disguise []DisguiseConfig `toml:"disguise"`
	Ports    []PortMap        `toml:"ports"`
}

// Duration wraps time.Duration so the TOML file can say "5s" / "500ms"
// instead of a raw nanosecond integer.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(b), err)
	}
	d.Duration = v
	return nil
}

// MarshalText is what makes saveConfig's toml.Encoder (tunnels.go, used by
// the "edit tunnel" menu) round-trip a Duration back to "5s" instead of a
// bare struct. It must use a value receiver, not a pointer one: the encoder
// checks whether the field's own type (Duration, not *Duration) implements
// encoding.TextMarshaler — a pointer-receiver method wouldn't count, and
// without this the encoder fell back to writing Duration's embedded
// time.Duration field as its own subtable ("[heartbeat]\n  Duration = ...",
// literally un-parseable — LoadConfig's own Undecoded() check then refused
// to read the file back, which is what made every edited tunnel need a
// delete-and-recreate to recover).
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}

// LoadConfig reads and validates a config file for role ("server" or
// "client"). Role has to be set before validate() runs — it's what decides
// which fields (listen_addr + ports for a server, server_addr for a client)
// are actually required — so it takes role as a parameter rather than
// leaving a caller to set cfg.Role on the result afterward, which would
// validate against an empty role and silently skip those checks.
func LoadConfig(path, role string) (*Config, error) {
	cfg := defaultConfig()
	cfg.Role = role
	meta, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	// A key that decodes to nothing is not a harmless typo here: TOML scopes
	// every bare key to the most recently opened table, so a key placed
	// after a [[disguise]] or [[ports]] block silently becomes a field of
	// that array entry instead of the top-level Config it was meant for —
	// and since PortMap/DisguiseConfig don't have that field, the intended
	// setting just vanishes, the file still "loads" successfully, and the
	// tunnel quietly runs on whatever default that setting had. This is
	// exactly the bug that made early buffer-tuning changes never actually
	// take effect — see docs/TUNING.md. Undecoded() is the only thing that
	// catches it, so a config with any is refused rather than run wrong.
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config %s: %d setting(s) not recognized (check they aren't placed after a [[disguise]] or [[ports]] block, which silently swallows anything meant for the top level): %v", path, len(undecoded), undecoded)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// dialsOut reports whether this role, under this Direction, is the side
// that actively dials out to establish the control channel and pool/mux
// capacity (true), or the side that passively listens for it (false). See
// the Direction field's doc comment for the full picture; direct.go is
// where this actually changes behavior.
func (c *Config) dialsOut() bool {
	return (c.Role == "server") == (c.Direction == "direct")
}

func defaultConfig() *Config {
	return &Config{
		Mode:                 "tcpmux",
		Direction:            "reverse",
		Heartbeat:            Duration{5 * time.Second},
		RetryMin:             Duration{1 * time.Second},
		RetryMax:             Duration{30 * time.Second},
		Nodelay:              true,
		KeepAlive:            Duration{15 * time.Second},
		RecvBuf:              256 * 1024,
		SendBuf:              256 * 1024,
		DialTimeout:          Duration{8 * time.Second},
		BufferSize:           32 * 1024,
		MinIdle:              4,
		MaxIdle:              16,
		IdleGrace:            Duration{20 * time.Second},
		MaxStreamsPerSession: 64,
		MuxFrameSize:         32 * 1024,
		MuxRecvBuffer:        4 * 1024 * 1024,
		MuxStreamBuffer:      1 * 1024 * 1024,
		MuxKeepAlive:         Duration{10 * time.Second},
	}
}

func (c *Config) validate() error {
	if c.Mode != "tcp" && c.Mode != "tcpmux" && c.Mode != "udp" {
		return fmt.Errorf("mode must be \"tcp\", \"tcpmux\" or \"udp\", got %q", c.Mode)
	}
	if c.Direction != "reverse" && c.Direction != "direct" {
		return fmt.Errorf("direction must be \"reverse\" or \"direct\", got %q", c.Direction)
	}
	if c.Mode == "udp" && c.Direction != "reverse" {
		// The udp carrier's pool socket is inherently reverse-shaped — the
		// server listens for it, the client dials — the same way paqet's
		// own protocol is inherently direct-shaped. See udpcarrier.go.
		return fmt.Errorf("mode \"udp\" only supports direction \"reverse\" for now")
	}
	if c.Token == "" {
		return fmt.Errorf("token must not be empty")
	}
	if c.Mode == "tcpmux" && (c.MuxFrameSize <= 0 || c.MuxFrameSize > 65535) {
		return fmt.Errorf("mux_frame_size must be between 1 and 65535 (smux's frame header is 16-bit) — got %d; 65536 is a common mistake, use 65535", c.MuxFrameSize)
	}

	nEnabled := 0
	for i := range c.Disguise {
		d := &c.Disguise[i]
		if !d.Enabled {
			continue
		}
		nEnabled++
		switch d.Type {
		case "plain", "noise":
		case "wss":
			if d.Path == "" {
				d.Path = "/ws"
			}
		case "kcp":
			if d.KCPPreset != "" && d.KCPPreset != "custom" && !validKCPPreset(d.KCPPreset) {
				return fmt.Errorf("disguise[%d] (kcp): kcp_preset must be \"balance\", \"turbo\", \"aggressive\", \"throughput\", \"custom\" or blank, got %q", i, d.KCPPreset)
			}
			if (d.KCPPreset == "" || d.KCPPreset == "custom") && d.KCPDataShards > 0 && d.KCPParityShards == 0 {
				return fmt.Errorf("disguise[%d] (kcp): kcp_parityshards must be > 0 when kcp_datashards is set (0 disables FEC entirely — set both to 0, not just one)", i)
			}
		case "quic":
			// Reuses wss's cert_file/key_file/domain/insecure_skip_verify
			// fields as-is — nothing quic-specific to validate beyond what
			// the wss case already established the shape of.
		default:
			return fmt.Errorf("disguise[%d]: type must be \"plain\", \"noise\", \"wss\", \"kcp\" or \"quic\", got %q", i, d.Type)
		}
		// Which field is required tracks who actually listens vs. dials —
		// not raw Role, which under Direction "direct" has the "server"
		// role dialing out and the "client" role listening. See dialsOut().
		if c.dialsOut() {
			if d.ServerAddr == "" {
				return fmt.Errorf("disguise[%d] (%s): server_addr is required", i, d.Type)
			}
		} else {
			if d.ListenAddr == "" {
				return fmt.Errorf("disguise[%d] (%s): listen_addr is required", i, d.Type)
			}
		}
	}
	if nEnabled == 0 {
		return fmt.Errorf("at least one enabled [[disguise]] entry is required")
	}

	switch c.Role {
	case "server":
		if len(c.Ports) == 0 {
			return fmt.Errorf("server: at least one [[ports]] entry is required")
		}
	}
	if c.MinIdle < 1 {
		return fmt.Errorf("min_idle must be >= 1")
	}
	if c.MaxIdle < c.MinIdle {
		return fmt.Errorf("max_idle must be >= min_idle")
	}
	return nil
}
