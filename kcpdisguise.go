package main

// Disguise type "kcp": a reliable, ordered session over raw UDP via
// xtaci/kcp-go — the same library paqet and BackPack both build on —
// encrypted with a key derived from Token, tuned for the lowest steady
// latency a lossy link can offer at the cost of extra bandwidth on parity
// and immediate ACKs. Studied BackPack's own KCP transport and preset table
// (internal/{client,server}/transport/kcp.go, internal/manage/preset.go)
// before writing this — same preset names and numbers, original
// implementation.
//
// Architecturally this needs nothing new anywhere else in the project:
// kcp-go's UDPSession, with SetStreamMode(true), behaves exactly like a
// reliable byte stream — so dialKCP/startKCPListener just produce a
// net.Conn the same way dialDisguise's plain/noise/wss cases already do,
// and every line of handshake, pool, and mux code downstream of that runs
// completely unchanged.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"

	"github.com/xtaci/kcp-go/v5"
)

// kcpPreset is one named point in the tradeoff between latency and
// bandwidth — see applyKCPPreset for the numbers and the reasoning behind
// each one, studied from BackPack's own preset.go.
type kcpPreset struct {
	name  string
	blurb string
}

var kcpPresets = []kcpPreset{
	{"balance", "light on CPU/RAM — small or shared VPS, ~51 Mbit/s headroom"},
	{"turbo", "recommended default — ~102 Mbit/s headroom, most Iran-to-abroad links"},
	{"aggressive", "maximum gaming headroom on a strong server, noticeably more CPU"},
	{"throughput", "NOT a gaming preset — trades steady ping for maximum bandwidth"},
	{"custom", "enter every value by hand"},
}

func validKCPPreset(name string) bool {
	for _, p := range kcpPresets {
		if p.name == name && p.name != "custom" {
			return true
		}
	}
	return false
}

// applyKCPPreset fills every KCP field of d from the named preset — both
// ends must agree on all of it, since none of it is negotiated on the wire.
// The numbers are BackPack's own (internal/manage/preset.go's
// applyKCPPreset), reproduced here rather than re-derived, since they
// reflect real-world tuning this project has no independent basis to
// second-guess.
func applyKCPPreset(d *DisguiseConfig) {
	// Latency-first ARQ, identical on every preset except Throughput: NoDelay
	// skips the delayed-ACK wait, a 10ms tick (kcp-go's floor) flushes
	// retransmits promptly, Resend=2 fast-retransmits after 2 duplicate ACKs
	// instead of waiting on a timer, NoCongestion removes KCP's own AIMD
	// window so one loss doesn't halve the rate, and AckNoDelay returns each
	// ACK immediately so the sender learns of loss a round trip sooner.
	d.KCPMTU = 1250 // below the common 1500 path MTU, with room for FEC's own padding — see kcpPresets' doc comment
	d.KCPInterval = 10
	d.KCPResend = 2
	d.KCPNoDelay = 1
	d.KCPNoCongestion = 1
	d.KCPAckNoDelay = true

	switch d.KCPPreset {
	case "balance":
		d.KCPSndWnd, d.KCPRcvWnd = 512, 512
		d.KCPDataShards, d.KCPParityShards = 10, 2 // ~17% loss tolerated, 20% overhead
	case "turbo":
		d.KCPSndWnd, d.KCPRcvWnd = 1024, 1024
		d.KCPDataShards, d.KCPParityShards = 10, 3 // ~33% loss tolerated, 30% overhead
	case "aggressive":
		d.KCPSndWnd, d.KCPRcvWnd = 2048, 2048
		d.KCPDataShards, d.KCPParityShards = 10, 4 // ~40% loss tolerated, 40% overhead
	case "throughput":
		// Everything here undoes a latency-first default above — not a
		// gaming preset: batches ACKs, ticks half as often, and opens the
		// window wide enough that one stream can fill a long path.
		d.KCPInterval = 20
		d.KCPAckNoDelay = false
		d.KCPSndWnd, d.KCPRcvWnd = 4096, 4096
		d.KCPDataShards, d.KCPParityShards = 10, 1 // ~10% overhead — ARQ handles the rest
	}
}

// kcpCrypt derives the KCP block cipher from the tunnel token — KCP has no
// handshake of its own to prove identity, so this both encrypts every
// datagram and makes the session unreadable to anyone who doesn't already
// know the token, on top of (not instead of) protocol.go's own HMAC
// challenge/response, which still runs over whatever this produces.
func kcpCrypt(token string) (kcp.BlockCrypt, error) {
	key := sha256.Sum256([]byte("rmtunnel-kcp-v1:" + token))
	block, err := kcp.NewAESBlockCrypt(key[:])
	if err != nil {
		return nil, fmt.Errorf("kcp: deriving cipher: %w", err)
	}
	return block, nil
}

// applyKCPTuning pushes d's settings onto a live session — called on every
// dialed and every accepted one, both ends.
func applyKCPTuning(sess *kcp.UDPSession, d *DisguiseConfig, cfg *Config) {
	sess.SetNoDelay(d.KCPNoDelay, d.KCPInterval, d.KCPResend, d.KCPNoCongestion)
	sess.SetWindowSize(d.KCPSndWnd, d.KCPRcvWnd)
	sess.SetMtu(d.KCPMTU)
	// What rides on this session (this project's own handshake, then pool
	// bytes or a smux stream) is a byte stream with no message boundaries
	// of its own to preserve — same reasoning as BackPack's own comment on
	// this exact call — so full segments beat one segment per Write.
	sess.SetStreamMode(true)
	sess.SetWriteDelay(false)
	sess.SetACKNoDelay(d.KCPAckNoDelay)
	sess.SetDSCP(46) // Expedited Forwarding — routers that honor it treat this as low-latency traffic; ignored elsewhere
	if cfg.RecvBuf > 0 {
		sess.SetReadBuffer(cfg.RecvBuf)
	}
	if cfg.SendBuf > 0 {
		sess.SetWriteBuffer(cfg.SendBuf)
	}
}

// dialKCP opens one KCP session to d.ServerAddr (or a backup — see
// profiles.go, the caller already resolved which address this is).
func dialKCP(ctx context.Context, cfg *Config, d *DisguiseConfig) (net.Conn, error) {
	block, err := kcpCrypt(cfg.Token)
	if err != nil {
		return nil, err
	}
	sess, err := kcp.DialWithOptions(d.ServerAddr, block, d.KCPDataShards, d.KCPParityShards)
	if err != nil {
		return nil, fmt.Errorf("kcp: dialing %s: %w", d.ServerAddr, err)
	}
	applyKCPTuning(sess, d, cfg)
	return sess, nil
}

// kcpAccepter adapts a *kcp.Listener to this project's accepter interface
// (see disguise.go) — AcceptKCP returns a concrete *UDPSession rather than
// a net.Conn, so Go needs the wrapper to satisfy the interface signature.
type kcpAccepter struct {
	ln  *kcp.Listener
	cfg *Config
	d   *DisguiseConfig
}

func (a *kcpAccepter) Accept() (net.Conn, error) {
	sess, err := a.ln.AcceptKCP()
	if err != nil {
		return nil, err
	}
	applyKCPTuning(sess, a.d, a.cfg)
	return sess, nil
}

func (a *kcpAccepter) Close() error { return a.ln.Close() }

func startKCPListener(cfg *Config, d *DisguiseConfig) (accepter, error) {
	block, err := kcpCrypt(cfg.Token)
	if err != nil {
		return nil, err
	}
	ln, err := kcp.ListenWithOptions(d.ListenAddr, block, d.KCPDataShards, d.KCPParityShards)
	if err != nil {
		return nil, fmt.Errorf("kcp: listening on %s: %w", d.ListenAddr, err)
	}
	if cfg.RecvBuf > 0 {
		ln.SetReadBuffer(cfg.RecvBuf)
	}
	if cfg.SendBuf > 0 {
		ln.SetWriteBuffer(cfg.SendBuf)
	}
	ln.SetDSCP(46)
	return &kcpAccepter{ln: ln, cfg: cfg, d: d}, nil
}
