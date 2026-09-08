package main

// Mode "udp" is a third carrier, alongside "tcp" and "tcpmux": the tunnel
// relays end-user UDP traffic as raw datagrams, one packet in becoming one
// packet out end to end, with no framing, no retransmission, and no backing
// byte stream — for UDP application protocols (WireGuard, a game) that
// already tolerate loss and want the tunnel adding as little overhead as
// possible. This is deliberately the plainest of the three "UDP variant"
// options studied from BackPack (raw / KCP+FEC / QUIC) — the other two stay
// previews (see wizard.go's askUDPFamilyPreview) because they are each a
// much larger, separate undertaking, matching this project's own precedent
// of wrapping paqet rather than reimplementing that ground twice.
//
// The control channel is completely unchanged — still TCP, over whatever
// disguise is configured, same handshake, same epoch, same profile
// failover. Only pool capacity moves to a dedicated UDP socket pair, reused
// on the server side (one shared listening socket, flows demultiplexed by
// source address, mirroring how a real UDP tunnel has to work since UDP
// itself has no connection concept) and per-slot on the client side (one
// dialed UDP socket per pool member, mirroring tcpPoolWorker/
// muxSessionWorker so the existing MinIdle/MaxIdle maintainer needs no
// changes at all — see client.go's spawnOne).
//
// Forwarded ports in this mode are always UDP, regardless of a [[ports]]
// entry's own udp field — there is no TCP carrier alongside to also forward
// TCP over, unlike tcp/tcpmux mode where udp=true adds UDP relaying on top
// of an otherwise-TCP forward (see udp.go, a different mechanism for a
// different mode).
//
// The pool socket binds to the first enabled [[disguise]]'s address (same
// host:port as its TCP listener — UDP and TCP share a port number without
// conflict, being different protocols). Mode "udp" is meant to pair with a
// single "plain" disguise: noise/wss add TCP-specific framing that protects
// the control channel already, and has nothing left to add to a pool socket
// that carries no secrets beyond the per-flow auth tag below.

import (
	"context"
	"crypto/subtle"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// udpAuthTag is a UDP pool socket's entire handshake: one fixed-size
// datagram proving the sender knows the token for the control channel's
// current epoch. A challenge/response round trip (like the TCP pool's) adds
// a full RTT per socket for no real benefit here — UDP pool capacity is
// cheap and plentiful, and a lost auth datagram just means that one socket
// never gets claimed and the maintainer dials a replacement, the same way a
// dropped TCP pool dial already fails silently and gets retried. The token
// itself still never crosses the wire, matching protocol.go's own design.
func udpAuthTag(token string, epoch []byte) []byte {
	return hmacTag(token, epoch)
}

func tuneUDPConn(conn *net.UDPConn, rcvBuf, sndBuf int) {
	if rcvBuf > 0 {
		conn.SetReadBuffer(rcvBuf)
	}
	if sndBuf > 0 {
		conn.SetWriteBuffer(sndBuf)
	}
}

// --- server side -----------------------------------------------------------

// udpPoolFlow is one authenticated client pool socket, known to the server
// only as a peer address on the shared pool socket — UDP has no connection
// object of its own to hold onto.
type udpPoolFlow struct {
	peer       *net.UDPAddr
	inbound    chan []byte
	lastActive int64 // unix nano, atomic
}

// udpCarrierSession is one end-user UDP flow (identified by its source
// address on a forwarded port) paired with the pool flow currently serving
// it.
type udpCarrierSession struct {
	userAddr   *net.UDPAddr
	flow       *udpPoolFlow
	toFlow     chan []byte
	lastActive int64 // unix nano, atomic
}

// runUDPCarrier is Server's entry point for Mode "udp": bring up the shared
// pool socket and every forwarded port's own UDP listener. Ports are
// started here rather than in Run's usual loop because this mode's ports
// are always UDP-only — see the package doc comment above.
//
// Every socket in this file resolves and binds "udp4" explicitly, not the
// family-agnostic "udp" — an unspecified address ("0.0.0.0:PORT") given to
// the generic network still resolves to a dual-stack "[::]:PORT" bind on
// most systems, and if that box's kernel has IPv6 disabled or
// net.ipv6.bindv6only=1 set, a socket bound that way silently never
// receives the IPv4 traffic this project's config format is entirely
// written in terms of. "udp4" removes the ambiguity outright rather than
// depending on a kernel default matching what was intended.
func (s *Server) runUDPCarrier(ctx context.Context) {
	addr := s.firstDisguiseAddr()
	if addr == "" {
		log.Fatalf("[udp] no enabled [[disguise]] entry to bind the pool socket to")
	}
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		log.Fatalf("[udp] resolving pool address %s: %v", addr, err)
	}
	sock, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		log.Fatalf("[udp] listening on %s: %v", addr, err)
	}
	tuneUDPConn(sock, s.cfg.RecvBuf, s.cfg.SendBuf)
	s.udpSock = sock
	log.Printf("[udp] pool socket listening on %s", sock.LocalAddr())

	go func() {
		<-ctx.Done()
		sock.Close()
	}()
	go s.udpPoolReadLoop(ctx, sock)
	go s.udpFlowReaper(ctx)

	for _, pm := range s.cfg.Ports {
		go s.runUDPCarrierPort(ctx, pm)
	}

	<-ctx.Done()
}

// firstDisguiseAddr is the address the udp-carrier pool socket binds to —
// see the package doc comment for why it's the first enabled disguise's.
func (s *Server) firstDisguiseAddr() string {
	for i := range s.cfg.Disguise {
		if s.cfg.Disguise[i].Enabled {
			return s.cfg.Disguise[i].ListenAddr
		}
	}
	return ""
}

// udpPoolReadLoop is the one goroutine that ever reads the shared pool
// socket: every client pool member's traffic arrives here, demultiplexed by
// source address into either a brand new flow's auth check or an existing
// flow's inbound channel.
func (s *Server) udpPoolReadLoop(ctx context.Context, sock *net.UDPConn) {
	buf := make([]byte, maxUDPDatagram)
	for {
		n, peer, err := sock.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		key := peer.String()

		s.udpMu.Lock()
		flow, ok := s.udpFlows[key]
		s.udpMu.Unlock()

		if ok {
			atomic.StoreInt64(&flow.lastActive, time.Now().UnixNano())
			data := append([]byte(nil), buf[:n]...)
			select {
			case flow.inbound <- data:
			default: // consumer behind; drop rather than block every other peer
			}
			continue
		}

		// An address we haven't seen: the only valid first datagram is the
		// auth tag for the control channel's current epoch.
		want := udpAuthTag(s.cfg.Token, s.currentEpoch())
		if n != len(want) || subtle.ConstantTimeCompare(buf[:n], want) != 1 {
			continue // not a match — say nothing, same as a bad TCP pool auth
		}

		newFlow := &udpPoolFlow{peer: peer, inbound: make(chan []byte, 64)}
		atomic.StoreInt64(&newFlow.lastActive, time.Now().UnixNano())
		s.udpMu.Lock()
		s.udpFlows[key] = newFlow
		s.udpMu.Unlock()

		select {
		case s.udpFlowCh <- newFlow:
		default:
			// Pool channel is full — the maintainer overshot demand; drop
			// this one and let the client's own pool eventually settle.
			s.udpMu.Lock()
			delete(s.udpFlows, key)
			s.udpMu.Unlock()
		}
	}
}

// udpFlowReaper retires pool flows nothing has used in a while — a claimed
// flow that finished its session already removes itself (see
// serveUDPCarrierSession), so this is only for auth'd-but-never-claimed
// flows sitting idle.
func (s *Server) udpFlowReaper(ctx context.Context) {
	ticker := time.NewTicker(udpReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().UnixNano()
			s.udpMu.Lock()
			for key, f := range s.udpFlows {
				if now-atomic.LoadInt64(&f.lastActive) > int64(udpIdleTimeout) {
					delete(s.udpFlows, key)
				}
			}
			s.udpMu.Unlock()
		}
	}
}

// obtainUDPFlow claims one authenticated pool flow and announces target to
// it as that flow's first datagram — the udp-carrier equivalent of
// obtainCarrier + writeString(carrier, target), just datagram-shaped
// instead of stream-shaped.
func (s *Server) obtainUDPFlow(ctx context.Context, target string) *udpPoolFlow {
	for {
		select {
		case f := <-s.udpFlowCh:
			if _, err := s.udpSock.WriteToUDP([]byte(target), f.peer); err != nil {
				continue
			}
			return f
		default:
		}
		s.requestMore()
		select {
		case f := <-s.udpFlowCh:
			if _, err := s.udpSock.WriteToUDP([]byte(target), f.peer); err != nil {
				continue
			}
			return f
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return nil
		}
	}
}

// runUDPCarrierPort is one forwarded port's UDP listener: every source
// address is its own end-user flow, paired on first sight with a freshly
// claimed pool flow and relayed for as long as either side keeps sending.
func (s *Server) runUDPCarrierPort(ctx context.Context, pm PortMap) {
	addr, err := net.ResolveUDPAddr("udp4", pm.Listen)
	if err != nil {
		log.Fatalf("[udp] resolving %s: %v", pm.Listen, err)
	}
	sock, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Fatalf("[udp] listening on %s: %v", pm.Listen, err)
	}
	tuneUDPConn(sock, s.cfg.RecvBuf, s.cfg.SendBuf)
	defer sock.Close()
	log.Printf("[udp] forwarding %s -> (client dials) %s", pm.Listen, pm.Target)

	go func() {
		<-ctx.Done()
		sock.Close()
	}()

	var mu sync.Mutex
	sessions := make(map[string]*udpCarrierSession)

	go func() {
		ticker := time.NewTicker(udpReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now().UnixNano()
				mu.Lock()
				for key, sess := range sessions {
					if now-atomic.LoadInt64(&sess.lastActive) > int64(udpIdleTimeout) {
						delete(sessions, key)
					}
				}
				mu.Unlock()
			}
		}
	}()

	buf := make([]byte, maxUDPDatagram)
	for {
		n, userAddr, err := sock.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		key := userAddr.String()

		mu.Lock()
		sess, ok := sessions[key]
		mu.Unlock()

		if ok {
			atomic.StoreInt64(&sess.lastActive, time.Now().UnixNano())
			data := append([]byte(nil), buf[:n]...)
			select {
			case sess.toFlow <- data:
			default:
			}
			continue
		}

		obtainCtx, cancel := context.WithTimeout(ctx, s.cfg.DialTimeout.Duration+5*time.Second)
		flow := s.obtainUDPFlow(obtainCtx, pm.Target)
		cancel()
		if flow == nil {
			continue
		}

		sess = &udpCarrierSession{userAddr: userAddr, flow: flow, toFlow: make(chan []byte, 64)}
		atomic.StoreInt64(&sess.lastActive, time.Now().UnixNano())
		mu.Lock()
		sessions[key] = sess
		mu.Unlock()

		firstPacket := append([]byte(nil), buf[:n]...)
		sess.toFlow <- firstPacket

		go s.serveUDPCarrierSession(ctx, sock, sess, sessions, &mu)
	}
}

func (s *Server) serveUDPCarrierSession(ctx context.Context, portSock *net.UDPConn, sess *udpCarrierSession, sessions map[string]*udpCarrierSession, mu *sync.Mutex) {
	defer func() {
		mu.Lock()
		if sessions[sess.userAddr.String()] == sess {
			delete(sessions, sess.userAddr.String())
		}
		mu.Unlock()
		s.udpMu.Lock()
		delete(s.udpFlows, sess.flow.peer.String())
		s.udpMu.Unlock()
	}()

	idle := time.NewTimer(udpIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-sess.toFlow:
			if n, err := s.udpSock.WriteToUDP(data, sess.flow.peer); err == nil {
				atomic.AddInt64(&totalBytesTransferred, int64(n))
			}
			resetTimer(idle, udpIdleTimeout)
		case data := <-sess.flow.inbound:
			if n, err := portSock.WriteToUDP(data, sess.userAddr); err == nil {
				atomic.AddInt64(&totalBytesTransferred, int64(n))
			}
			resetTimer(idle, udpIdleTimeout)
		case <-idle.C:
			return
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// --- client side -------------------------------------------------------

// udpCarrierSpawnOne is one udp-mode pool member: dial the server's pool
// socket, present the auth tag, wait to be claimed (told a target), then
// relay raw datagrams between the server and that local target until
// either side goes quiet. Called from Client's existing maintainer/
// spawnOne (see client.go) exactly like tcpPoolWorker/muxSessionWorker, so
// MinIdle/MaxIdle pool sizing needs no changes at all for this mode.
func (c *Client) udpCarrierSpawnOne(ctx context.Context, p *profileState, epoch []byte) {
	atomic.AddInt32(&c.activeCount, 1)
	defer atomic.AddInt32(&c.activeCount, -1)

	remote, err := net.ResolveUDPAddr("udp4", p.addr())
	if err != nil {
		return
	}
	sock, err := net.DialUDP("udp4", nil, remote)
	if err != nil {
		return
	}
	defer sock.Close()
	tuneUDPConn(sock, c.cfg.RecvBuf, c.cfg.SendBuf)

	if _, err := sock.Write(udpAuthTag(c.cfg.Token, epoch)); err != nil {
		return
	}

	// Idle, waiting to be claimed. A watcher lets a generation shutdown
	// interrupt this the same way tcpPoolWorker's does for an idle TCP pool
	// connection blocked reading its target — plus its own bounded timeout,
	// since unlike a TCP pool connection (which shrinkOne can close on
	// demand once MaxIdle is exceeded) this mode has no such signal wired
	// up yet: a claim-wait with no timeout at all would let an
	// over-provisioned pool's idle members live for the whole generation
	// instead of ever being retired.
	const claimWait = 5 * time.Minute
	sock.SetReadDeadline(time.Now().Add(claimWait))
	watcherDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			sock.SetDeadline(time.Now())
		case <-watcherDone:
		}
	}()

	buf := make([]byte, maxUDPDatagram)
	n, err := sock.Read(buf)
	close(watcherDone)
	if err != nil {
		return // never claimed within claimWait — a normal idle pool member being retired
	}
	sock.SetReadDeadline(time.Time{})
	target := string(buf[:n])

	localAddr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return
	}
	local, err := net.DialUDP("udp4", nil, localAddr)
	if err != nil {
		return
	}
	defer local.Close()
	tuneUDPConn(local, c.cfg.RecvBuf, c.cfg.SendBuf)

	udpCarrierCopy(sock, local)
}

// udpCarrierCopy relays raw datagrams both ways until one side goes quiet
// for udpIdleTimeout or errors — the udp-carrier equivalent of Pipe, but
// message-for-message rather than stream-chunked, since that is what
// preserves a UDP application's own packet boundaries end to end.
func udpCarrierCopy(a, b *net.UDPConn) {
	done := make(chan struct{}, 2)
	go func() { udpCarrierPump(a, b); done <- struct{}{} }()
	go func() { udpCarrierPump(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}

func udpCarrierPump(src, dst *net.UDPConn) {
	buf := make([]byte, maxUDPDatagram)
	for {
		src.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		n, err := src.Read(buf)
		if err != nil {
			return
		}
		w, err := dst.Write(buf[:n])
		if err != nil {
			return
		}
		atomic.AddInt64(&totalBytesTransferred, int64(w))
	}
}
