package main

import (
	"context"
	"errors"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"
)

// clientSession tracks one mux session this client keeps open toward the
// server, and how many streams the server currently has open on it — needed
// so shrinkOne never closes a session that is actually carrying traffic.
type clientSession struct {
	session *smux.Session
	streams int32
	stop    chan struct{}
}

// Client is the Kharej-side role: it dials out to the server, keeps a
// control channel alive with reconnect/backoff across whichever disguise
// profile currently looks healthiest (see profiles.go), and maintains a pool
// of ready pool connections or mux sessions so the server never waits on the
// tunnel-side handshake when a real user connects.
type Client struct {
	cfg      *Config
	profiles []*profileState

	mu          sync.Mutex
	controlConn net.Conn
	epoch       []byte
	curProfile  *profileState
	rttMicros   int64 // atomic; microseconds, not milliseconds — a loopback or LAN RTT rounds to 0ms and would otherwise look unmeasured

	activeCount int32

	tcpIdleMu sync.Mutex
	tcpIdle   map[net.Conn]chan struct{}

	sessMu   sync.Mutex
	sessions []*clientSession
}

func NewClient(cfg *Config) *Client {
	return &Client{
		cfg:      cfg,
		profiles: buildProfiles(cfg),
		tcpIdle:  make(map[net.Conn]chan struct{}),
	}
}

// Run starts this Client's control+pool acquisition strategy: dialing out
// with profile failover (Direction "reverse", the usual case), or listening
// for the peer to dial in instead (Direction "direct" — see direct.go).
// Either way, every carrier that results is handed to serveTarget the same
// way once it's obtained.
func (c *Client) Run(ctx context.Context) {
	if !c.cfg.dialsOut() {
		c.runListener(ctx)
		return
	}
	bo := newBackoff(c.cfg.RetryMin.Duration, c.cfg.RetryMax.Duration)
	for ctx.Err() == nil {
		p := pickProfile(c.profiles)
		startedAt := time.Now()
		established, err := c.runOnce(ctx, p)
		if ctx.Err() != nil {
			return
		}
		lived := time.Since(startedAt)
		label := p.label() // before any rotation below, so this log names the address that was actually just tried

		switch {
		case errors.Is(err, errDegraded):
			p.onFailure(false)
			p.rotateAddr()
		case established && lived >= shortLivedThreshold:
			p.onSuccess()
			bo.reset()
		case established:
			p.onFailure(true) // connected, then died fast — likely interference
			p.rotateAddr()
		default:
			p.onFailure(false) // never even connected
			p.rotateAddr()
		}
		log.Printf("control channel [%s] ended after %s: %v — reconnecting", label, lived.Round(time.Second), err)
		bo.wait(ctx)
	}
}

// runOnce owns one control-channel generation against profile p. It reports
// whether the control handshake ever completed, so Run can tell "never
// reached the server" apart from "reached it, then something went wrong" —
// the two cases the profile's health tracking treats very differently.
func (c *Client) runOnce(ctx context.Context, p *profileState) (established bool, err error) {
	conn, err := dialProfile(ctx, c.cfg, p)
	if err != nil {
		return false, err
	}
	epoch, err := clientHandshakeControl(conn, c.cfg.Token)
	if err != nil {
		conn.Close()
		return false, err
	}
	log.Printf("control channel established via [%s] (mode=%s)", p.label(), c.cfg.Mode)

	genCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	c.mu.Lock()
	c.controlConn = conn
	c.epoch = epoch
	c.curProfile = p
	c.mu.Unlock()

	needConn := make(chan struct{}, 64)
	go c.maintainer(genCtx, needConn)

	// Every heartbeat this end sends doubles as an RTT probe: pingSentAt is
	// stamped right before the write, and the reply — sigPong — lets the
	// read loop below compute the round trip. See profiles.go for why a
	// sustained bad RTT (not just a hard disconnect) is itself grounds to
	// try a different profile.
	var pingSentAt int64
	go func() {
		ticker := time.NewTicker(c.cfg.Heartbeat.Duration)
		defer ticker.Stop()
		for {
			select {
			case <-genCtx.Done():
				return
			case <-ticker.C:
				atomic.StoreInt64(&pingSentAt, time.Now().UnixNano())
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if writeByte(conn, sigHeartbeat) != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	var lastErr error
	badStreak := 0
readLoop:
	for {
		conn.SetReadDeadline(time.Now().Add(3 * c.cfg.Heartbeat.Duration))
		b, err := readByte(conn)
		if err != nil {
			lastErr = err
			break readLoop
		}
		switch b {
		case sigHeartbeat:
			// the server's own liveness beacon — nothing to do but have seen it

		case sigPong:
			sent := atomic.LoadInt64(&pingSentAt)
			if sent == 0 {
				continue
			}
			rtt := time.Since(time.Unix(0, sent))
			atomic.StoreInt64(&c.rttMicros, rtt.Microseconds())
			if rtt.Milliseconds() > degradedRTTMs {
				badStreak++
				if badStreak >= degradedStreak {
					lastErr = errDegraded
					break readLoop
				}
			} else {
				badStreak = 0
			}

		case sigNeedConn:
			select {
			case needConn <- struct{}{}:
			default:
			}

		case sigClose:
			lastErr = errors.New("server requested close")
			break readLoop
		}
	}

	cancel()
	conn.Close()
	c.mu.Lock()
	c.controlConn = nil
	c.epoch = nil
	c.curProfile = nil
	c.mu.Unlock()
	return true, lastErr
}

// maintainer keeps this generation's pool at MinIdle..MaxIdle, reacting
// immediately to NEED_CONN and topping up or trimming on a slow tick
// otherwise. See docs/TUNING.md for how to retune these.
func (c *Client) maintainer(ctx context.Context, needConn <-chan struct{}) {
	for i := 0; i < c.cfg.MinIdle; i++ {
		c.spawnOne(ctx)
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var overSince time.Time

	for {
		select {
		case <-ctx.Done():
			return

		case <-needConn:
			c.spawnOne(ctx)

		case <-ticker.C:
			n := int(atomic.LoadInt32(&c.activeCount))
			switch {
			case n < c.cfg.MinIdle:
				for i := n; i < c.cfg.MinIdle; i++ {
					c.spawnOne(ctx)
				}
				overSince = time.Time{}
			case n > c.cfg.MaxIdle:
				if overSince.IsZero() {
					overSince = time.Now()
				} else if time.Since(overSince) > c.cfg.IdleGrace.Duration {
					c.shrinkOne()
					overSince = time.Time{}
				}
			default:
				overSince = time.Time{}
			}
		}
	}
}

func (c *Client) spawnOne(ctx context.Context) {
	c.mu.Lock()
	epoch := c.epoch
	profile := c.curProfile
	c.mu.Unlock()
	if epoch == nil || profile == nil {
		return
	}
	switch c.cfg.Mode {
	case "tcp":
		go c.tcpPoolWorker(ctx, profile, epoch)
	case "tcpmux":
		go c.muxSessionWorker(ctx, profile, epoch)
	case "udp":
		go c.udpCarrierSpawnOne(ctx, profile, epoch)
	}
}

func (c *Client) shrinkOne() {
	switch c.cfg.Mode {
	case "tcp":
		c.tcpIdleMu.Lock()
		for conn, stop := range c.tcpIdle {
			delete(c.tcpIdle, conn)
			close(stop)
			break // exactly one
		}
		c.tcpIdleMu.Unlock()

	case "tcpmux":
		c.sessMu.Lock()
		for _, cs := range c.sessions {
			if atomic.LoadInt32(&cs.streams) == 0 {
				close(cs.stop)
				break
			}
		}
		c.sessMu.Unlock()
	}
}

// --- TCP mode ---------------------------------------------------------

func (c *Client) tcpPoolWorker(ctx context.Context, p *profileState, epoch []byte) {
	atomic.AddInt32(&c.activeCount, 1)
	defer atomic.AddInt32(&c.activeCount, -1)

	conn, err := dialProfile(ctx, c.cfg, p)
	if err != nil {
		return
	}
	if err := clientHandshakePool(conn, c.cfg.Token, epoch); err != nil {
		conn.Close()
		return
	}

	stop := make(chan struct{})
	c.tcpIdleMu.Lock()
	c.tcpIdle[conn] = stop
	c.tcpIdleMu.Unlock()
	unregister := func() {
		c.tcpIdleMu.Lock()
		delete(c.tcpIdle, conn)
		c.tcpIdleMu.Unlock()
	}

	// This connection is idle now, blocked reading the target address the
	// server will eventually send. Both a generation shutdown and a
	// deliberate shrink must be able to interrupt that read.
	watcherDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.SetDeadline(time.Now())
		case <-stop:
			conn.SetDeadline(time.Now())
		case <-watcherDone:
		}
	}()

	target, err := readString(conn)
	close(watcherDone)
	unregister()
	if err != nil {
		conn.Close()
		return
	}
	c.serveTarget(ctx, conn, target)
}

// serveTarget acts on the target address a carrier (a raw pool connection in
// tcp mode, a mux stream in tcpmux mode) was just told to reach — relay UDP,
// or dial the real backend over TCP and pipe. This is the one place "what
// does this side of the tunnel actually do with a carrier" lives, shared by
// every way a carrier can arrive: dialed out (tcpPoolWorker above), accepted
// as a mux stream (handleStream below), or accepted directly because this
// box is listening under Direction "direct" (see direct.go).
func (c *Client) serveTarget(ctx context.Context, carrier net.Conn, target string) {
	if addr, isUDP := isUDPTarget(target); isUDP {
		c.handleUDPCarrier(ctx, carrier, addr)
		return
	}
	local, err := dialLocal(ctx, c.cfg, target)
	if err != nil {
		carrier.Close()
		return
	}
	Pipe(local, carrier, c.cfg.BufferSize)
}

// --- TCPMux mode --------------------------------------------------------

func (c *Client) muxSessionWorker(ctx context.Context, p *profileState, epoch []byte) {
	atomic.AddInt32(&c.activeCount, 1)
	defer atomic.AddInt32(&c.activeCount, -1)

	conn, err := dialProfile(ctx, c.cfg, p)
	if err != nil {
		return
	}
	if err := clientHandshakePool(conn, c.cfg.Token, epoch); err != nil {
		conn.Close()
		return
	}

	session, err := smux.Server(conn, muxConfig(c.cfg))
	if err != nil {
		conn.Close()
		return
	}

	cs := &clientSession{session: session, stop: make(chan struct{})}
	c.sessMu.Lock()
	c.sessions = append(c.sessions, cs)
	c.sessMu.Unlock()
	defer func() {
		c.sessMu.Lock()
		for i, s := range c.sessions {
			if s == cs {
				c.sessions = append(c.sessions[:i], c.sessions[i+1:]...)
				break
			}
		}
		c.sessMu.Unlock()
	}()

	go func() {
		select {
		case <-ctx.Done():
		case <-cs.stop:
		}
		session.Close()
	}()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return
		}
		atomic.AddInt32(&cs.streams, 1)
		go c.handleStream(ctx, cs, stream)
	}
}

func (c *Client) handleStream(ctx context.Context, cs *clientSession, stream *smux.Stream) {
	defer atomic.AddInt32(&cs.streams, -1)

	target, err := readString(stream)
	if err != nil {
		stream.Close()
		return
	}
	c.serveTarget(ctx, stream, target)
}

// Stats returns a short snapshot for the periodic status log in main.go.
func (c *Client) Stats() string {
	c.mu.Lock()
	connected := c.controlConn != nil
	profile := c.curProfile
	c.mu.Unlock()
	if !connected {
		return "disconnected  "
	}
	n := int(atomic.LoadInt32(&c.activeCount))
	label := "idle pool"
	if c.cfg.Mode == "tcpmux" {
		label = "sessions"
	}
	rttUs := atomic.LoadInt64(&c.rttMicros)
	out := "[" + profile.label() + "] " + label + "=" + strconv.Itoa(n) + "  "
	if rttUs > 0 {
		out += "rtt=" + strconv.FormatFloat(float64(rttUs)/1000, 'f', 1, 64) + "ms  "
	}
	return out
}
