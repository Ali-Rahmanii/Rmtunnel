package main

// Direction "direct" flips which side dials out for the control channel and
// pool/mux capacity, for when the "server" box's inbound port doesn't get
// through but its outbound does (blocked, NAT without a forward, etc.) — see
// Config.Direction's doc comment in config.go and Config.dialsOut().
//
// Deliberately NOT a parallel tunnel engine: Role keeps meaning exactly what
// it always has ("server" owns [[ports]] and forwards to a target; "client"
// resolves and dials that target) and every wire-level primitive (the auth
// handshake, admitPoolConn, muxConfig, serveTarget) is the same code either
// direction uses. All that's new here is which of the two roles runs the
// *active* half (dial out, profile failover, backoff — normally Client.Run)
// versus the *passive* half (listen, authenticate, dispatch — normally
// Server's acceptLoop/admitTunnelConn) of establishing that capacity.
//
//   reverse (default): server listens, client dials.
//   direct:             server dials,   client listens.

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"
)

// --- Server as the dialer (Direction "direct") ------------------------------

// runDialer is Server's control+pool acquisition strategy when it's the side
// dialing out. It's a close mirror of Client.Run/runOnce — same profile
// failover, same backoff, same heartbeat/RTT-based degradation check — since
// "dial out, keep a pool topped up, reconnect on failure" is exactly that
// same problem regardless of which role is doing it. The one difference:
// what happens to a freshly authenticated pool connection/session is
// s.admitPoolConn — the same function used when one arrives by being
// accepted — so it becomes capacity this box's own obtainCarrier hands to a
// real end-user connection on its [[ports]] listeners, completely unchanged.
func (s *Server) runDialer(ctx context.Context) {
	profiles := buildProfiles(s.cfg)
	if len(profiles) == 0 {
		log.Fatalf("[direct] no enabled [[disguise]] entries to dial")
	}
	bo := newBackoff(s.cfg.RetryMin.Duration, s.cfg.RetryMax.Duration)
	for ctx.Err() == nil {
		p := pickProfile(profiles)
		startedAt := time.Now()
		established, err := s.dialControlOnce(ctx, p)
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
			p.onFailure(true)
			p.rotateAddr()
		default:
			p.onFailure(false)
			p.rotateAddr()
		}
		log.Printf("[direct] control channel [%s] ended after %s: %v — reconnecting", label, lived.Round(time.Second), err)
		bo.wait(ctx)
	}
}

// dialControlOnce owns one control-channel generation on the dialing side —
// the direct-mode counterpart of Client's runOnce. Structurally identical to
// Server.runControl's writer/reader loop (same single-writer-goroutine
// pattern, required because not every disguise transport's underlying
// connection tolerates concurrent writers — see wss.go), just reached by
// dialing instead of being handed a conn from an accept.
func (s *Server) dialControlOnce(ctx context.Context, p *profileState) (established bool, err error) {
	conn, err := dialProfile(ctx, s.cfg, p)
	if err != nil {
		return false, err
	}
	epoch, err := clientHandshakeControl(conn, s.cfg.Token)
	if err != nil {
		conn.Close()
		return false, err
	}
	log.Printf("[direct] control channel established via [%s] (mode=%s)", p.label(), s.cfg.Mode)

	genCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.mu.Lock()
	s.controlConn = conn
	s.epoch = epoch
	s.curDisguise = p.cfg.Type
	s.mu.Unlock()

	go s.dialMaintainer(genCtx, p, epoch)

	var pingSentAt int64
	go func() {
		ticker := time.NewTicker(s.cfg.Heartbeat.Duration)
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
		conn.SetReadDeadline(time.Now().Add(3 * s.cfg.Heartbeat.Duration))
		b, err := readByte(conn)
		if err != nil {
			lastErr = err
			break readLoop
		}
		switch b {
		case sigHeartbeat:
			// the peer's own liveness beacon — nothing to do but have seen it

		case sigPong:
			sent := atomic.LoadInt64(&pingSentAt)
			if sent == 0 {
				continue
			}
			rtt := time.Since(time.Unix(0, sent))
			if rtt.Milliseconds() > degradedRTTMs {
				badStreak++
				if badStreak >= degradedStreak {
					lastErr = errDegraded
					break readLoop
				}
			} else {
				badStreak = 0
			}

		case sigClose:
			lastErr = errors.New("peer requested close")
			break readLoop
		}
	}

	cancel()
	conn.Close()
	s.mu.Lock()
	if s.controlConn == conn {
		s.controlConn = nil
		s.epoch = nil
		s.curDisguise = ""
	}
	s.mu.Unlock()
	s.teardownEpoch(epoch)
	return true, lastErr
}

// dialMaintainer keeps this generation's pool topped up toward MinIdle by
// dialing out — the direct-mode counterpart of Client.maintainer. There is
// no sigNeedConn signal to react to here: in reverse mode that's the server
// (passively holding the pool) asking its peer to open more, but in direct
// mode this box IS the one dialing, so it just keeps its own pool topped up
// on a timer instead of waiting to be asked. No shrink-when-idle logic
// either — over-provisioning a few idle pool connections is cheap, and
// MinIdle/MaxIdle are modest numbers even at the heaviest tier.
func (s *Server) dialMaintainer(ctx context.Context, p *profileState, epoch []byte) {
	spawn := func() {
		conn, err := dialProfile(ctx, s.cfg, p)
		if err != nil {
			return
		}
		if err := clientHandshakePool(conn, s.cfg.Token, epoch); err != nil {
			conn.Close()
			return
		}
		s.admitPoolConn(conn)
	}
	for i := 0; i < s.cfg.MinIdle; i++ {
		go spawn()
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for n := s.poolCount(); n < s.cfg.MinIdle; n++ {
				go spawn()
			}
		}
	}
}

// poolCount is however much capacity obtainCarrier currently has to hand
// out, in whichever unit this Mode counts in — connections for tcp, sessions
// for tcpmux (mirroring Stats()).
func (s *Server) poolCount() int {
	if s.cfg.Mode == "tcp" {
		return len(s.poolQueue)
	}
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	return len(s.sessions)
}

// --- Client as the listener (Direction "direct") ----------------------------

// runListener is Client's control+pool acquisition strategy when it's the
// side listening instead of dialing out — the direct-mode counterpart of
// Server's disguise-listener + acceptLoop + admitTunnelConn. Every carrier
// it accepts still ends up at serveTarget exactly the way a dialed-out one
// does; only how the carrier was obtained differs.
func (c *Client) runListener(ctx context.Context) {
	var enabled []*DisguiseConfig
	for i := range c.cfg.Disguise {
		if c.cfg.Disguise[i].Enabled {
			enabled = append(enabled, &c.cfg.Disguise[i])
		}
	}
	if len(enabled) == 0 {
		log.Fatalf("[direct] no enabled [[disguise]] entries to listen on")
	}

	var wg sync.WaitGroup
	for _, d := range enabled {
		acc, err := startDisguiseListener(c.cfg, d)
		if err != nil {
			log.Fatalf("[direct] disguise %s (%s): %v", d.Type, d.ListenAddr, err)
		}
		log.Printf("[direct] listening for [%s] on %s (mode=%s)", d.Type, d.ListenAddr, c.cfg.Mode)

		wg.Add(1)
		go func(acc accepter, d *DisguiseConfig) {
			defer wg.Done()
			c.acceptLoop(ctx, acc, d)
		}(acc, d)

		go func(acc accepter) {
			<-ctx.Done()
			acc.Close()
		}(acc)
	}

	<-ctx.Done()
	wg.Wait()
}

func (c *Client) acceptLoop(ctx context.Context, acc accepter, d *DisguiseConfig) {
	for {
		conn, err := acc.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[direct][%s] accept error: %v", d.Type, err)
			continue
		}
		go c.admitTunnelConn(ctx, conn, d)
	}
}

func (c *Client) currentEpoch() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

func (c *Client) admitTunnelConn(ctx context.Context, conn net.Conn, d *DisguiseConfig) {
	res, err := serverHandshake(conn, c.cfg.Token, c.currentEpoch)
	if err != nil {
		conn.Close()
		return
	}

	switch res.role {
	case roleControl:
		c.mu.Lock()
		if c.controlConn != nil {
			c.mu.Unlock()
			serverRefuse(conn, ackBusy)
			conn.Close()
			return
		}
		epoch, err := randomBytes(8)
		if err != nil {
			c.mu.Unlock()
			conn.Close()
			return
		}
		c.mu.Unlock()

		if err := serverAckControl(conn, epoch); err != nil {
			conn.Close()
			return
		}
		c.runListenerControl(ctx, conn, epoch, d)

	case rolePool:
		if err := serverAckPool(conn); err != nil {
			conn.Close()
			return
		}
		go c.serveAcceptedCarrier(ctx, conn)
	}
}

// runListenerControl owns one control-channel generation on the listening
// side — structurally identical to Server.runControl (same single-writer-
// goroutine pattern for the same concurrent-write-safety reason), minus the
// sigNeedConn/ctrlOut machinery: this box never asks for more capacity,
// because it doesn't consume any — the dialer (Server.dialMaintainer) tops
// up its own pool on its own initiative.
func (c *Client) runListenerControl(ctx context.Context, conn net.Conn, epoch []byte, d *DisguiseConfig) {
	log.Printf("[direct] control channel accepted from %s via [%s]", conn.RemoteAddr(), d.Type)

	outCh := make(chan byte, 8)
	c.mu.Lock()
	c.controlConn = conn
	c.epoch = epoch
	c.curProfile = c.findProfile(d)
	c.mu.Unlock()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		ticker := time.NewTicker(c.cfg.Heartbeat.Duration)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if writeByte(conn, sigHeartbeat) != nil {
					return
				}
			case b, ok := <-outCh:
				if !ok {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if writeByte(conn, b) != nil {
					return
				}
			}
		}
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(3 * c.cfg.Heartbeat.Duration))
		b, err := readByte(conn)
		if err != nil {
			log.Printf("[direct] control channel from %s ended: %v", conn.RemoteAddr(), err)
			break
		}
		if b == sigClose {
			log.Printf("[direct] control channel from %s closed by peer", conn.RemoteAddr())
			break
		}
		if b == sigHeartbeat {
			select {
			case outCh <- sigPong:
			case <-ctx.Done():
			}
		}
	}

	c.mu.Lock()
	if c.controlConn == conn {
		c.controlConn = nil
		c.epoch = nil
		c.curProfile = nil
	}
	c.mu.Unlock()
	close(outCh)
	<-writerDone
	conn.Close()
}

// findProfile returns the profileState buildProfiles already made for d
// (for Stats()'s label and so a listening carrier participates in the same
// cooldown bookkeeping as a dialed one), falling back to a fresh stub if
// none matches — which shouldn't happen since d always comes from
// c.cfg.Disguise, but a stub is harmless if it ever does.
func (c *Client) findProfile(d *DisguiseConfig) *profileState {
	for _, p := range c.profiles {
		if p.cfg == d {
			return p
		}
	}
	return &profileState{cfg: d}
}

// serveAcceptedCarrier is what an accepted rolePool connection becomes: in
// tcp mode, one carrier, served directly; in tcpmux mode, a mux session
// carrying many streams, each served as its own carrier — the exact same
// split Client.muxSessionWorker makes for a dialed-out session.
func (c *Client) serveAcceptedCarrier(ctx context.Context, conn net.Conn) {
	switch c.cfg.Mode {
	case "tcp":
		target, err := readString(conn)
		if err != nil {
			conn.Close()
			return
		}
		c.serveTarget(ctx, conn, target)

	case "tcpmux":
		session, err := smux.Server(conn, muxConfig(c.cfg))
		if err != nil {
			conn.Close()
			return
		}
		for {
			stream, err := session.AcceptStream()
			if err != nil {
				return
			}
			go func(stream *smux.Stream) {
				target, err := readString(stream)
				if err != nil {
					stream.Close()
					return
				}
				c.serveTarget(ctx, stream, target)
			}(stream)
		}
	}
}
