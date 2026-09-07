package main

import (
	"fmt"
	"time"

	"github.com/BurntSushi/toml"
)

// PortMap describes one forwarded port: the server listens on Listen (e.g.
// Iran box, publicly reachable) and, for every connection it accepts there,
// asks the client to dial Target (e.g. 127.0.0.1:8080 on the Kharej box, or
// any address the client machine can reach).
type PortMap struct {
	Listen string `toml:"listen"`
	Target string `toml:"target"`
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
//
// The client tries entries in the order they appear, skipping any still in
// a failure cool-down — see profiles.go. The server simply listens on every
// enabled entry at once, so whichever one the client picks is already live;
// no coordination message ever has to cross the (possibly filtered) network
// for a switch to take effect. See docs/CENSORSHIP.md for the reasoning.
type DisguiseConfig struct {
	Type    string `toml:"type"` // "plain" | "noise" | "wss"
	Enabled bool   `toml:"enabled"`

	ListenAddr string `toml:"listen_addr"` // server
	ServerAddr string `toml:"server_addr"` // client

	// wss-only
	Domain    string `toml:"domain"`    // sent as SNI / Host; also the self-signed cert's CN
	Path      string `toml:"path"`      // the one path that upgrades to the tunnel
	CertFile  string `toml:"cert_file"` // empty = generate a self-signed cert on the fly
	KeyFile   string `toml:"key_file"`
	DecoyRoot string `toml:"decoy_root"`           // empty = a small built-in placeholder page
	Insecure  bool   `toml:"insecure_skip_verify"` // client: skip cert verification (self-signed server certs)
}

// Config is shared by both roles; a field only one role reads is simply
// ignored by the other. One struct, one file format, one place to look.
type Config struct {
	// Role is set by the CLI flag, not the file, but kept here so the rest of
	// the program can treat it like any other setting.
	Role string `toml:"-"`

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

	// Disguise lists the candidate ways to carry the tunnel, most-preferred
	// first. At least one enabled entry is required.
	Disguise []DisguiseConfig `toml:"disguise"`

	// --- server-only ---
	Ports     []PortMap `toml:"ports"`
	Heartbeat Duration  `toml:"heartbeat"` // control-channel liveness ping

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

func LoadConfig(path string) (*Config, error) {
	cfg := defaultConfig()
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		Mode:                 "tcpmux",
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
	if c.Mode != "tcp" && c.Mode != "tcpmux" {
		return fmt.Errorf("mode must be \"tcp\" or \"tcpmux\", got %q", c.Mode)
	}
	if c.Token == "" {
		return fmt.Errorf("token must not be empty")
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
		default:
			return fmt.Errorf("disguise[%d]: type must be \"plain\", \"noise\" or \"wss\", got %q", i, d.Type)
		}
		switch c.Role {
		case "server":
			if d.ListenAddr == "" {
				return fmt.Errorf("disguise[%d] (%s): listen_addr is required", i, d.Type)
			}
		case "client":
			if d.ServerAddr == "" {
				return fmt.Errorf("disguise[%d] (%s): server_addr is required", i, d.Type)
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
