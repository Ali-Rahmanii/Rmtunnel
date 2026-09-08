package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"
)

// muxSession is one server-opened mux session together with how many streams
// are currently in flight on it, so the picker can favour the least-loaded
// session instead of piling every new flow onto the first one.
type muxSession struct {
	session *smux.Session
	streams int32
}

// Server is the Iran-side role: it owns the public listeners the outside
// world connects to, and borrows capacity — pool connections or mux sessions
// — that the client keeps standing by on the tunnel connection.
type Server struct {
	cfg *Config

	mu          sync.RWMutex
	controlConn net.Conn
	epoch       []byte
	ctrlOut     chan byte
	curDisguise string

	poolQueue chan net.Conn

	sessMu   sync.Mutex
	sessions []*muxSession

	resource *notifier

	// Mode "udp" only — see udpcarrier.go.
	udpSock   *net.UDPConn
	udpMu     sync.Mutex
	udpFlows  map[string]*udpPoolFlow
	udpFlowCh chan *udpPoolFlow
}

func NewServer(cfg *Config) *Server {
	return &Server{
		cfg:       cfg,
		poolQueue: make(chan net.Conn, 4096),
		resource:  newNotifier(),
		udpFlows:  make(map[string]*udpPoolFlow),
		udpFlowCh: make(chan *udpPoolFlow, 4096),
	}
}

// Run starts one listener per enabled [[disguise]] entry — all of them, all
// at once, for as long as the process runs. There is no "currently active
// profile" on the server side and nothing to switch: whichever disguise a
// client's next connection attempt uses, that listener is already up and
// already feeds the same admitTunnelConn. See profiles.go for the reasoning.
func (s *Server) Run(ctx context.Context) error {
	// Mode "udp" owns its ports itself — every one is UDP-only there, a
	// different mechanism from udp.go's "also relay UDP alongside TCP" — see
	// udpcarrier.go's package doc comment.
	if s.cfg.Mode == "udp" {
		go s.runUDPCarrier(ctx)
	} else {
		// Ports are always this box's job regardless of Direction — the
		// "server" role always owns [[ports]] and always exposes them to
		// real users; only how the control channel/pool capacity is
		// obtained changes. See direct.go.
		for _, pm := range s.cfg.Ports {
			go s.runPortListener(ctx, pm)
			if pm.UDP {
				go s.runUDPListener(ctx, pm)
			}
		}
	}

	if s.cfg.dialsOut() {
		s.runDialer(ctx)
		return nil
	}

	var enabled []*DisguiseConfig
	for i := range s.cfg.Disguise {
		if s.cfg.Disguise[i].Enabled {
			enabled = append(enabled, &s.cfg.Disguise[i])
		}
	}
	if len(enabled) == 0 {
		return fmt.Errorf("no enabled [[disguise]] entries")
	}

	var wg sync.WaitGroup
	for _, d := range enabled {
		acc, err := startDisguiseListener(s.cfg, d)
		if err != nil {
			return fmt.Errorf("disguise %s (%s): %w", d.Type, d.ListenAddr, err)
		}
		log.Printf("listening for [%s] on %s (mode=%s)", d.Type, d.ListenAddr, s.cfg.Mode)

		wg.Add(1)
		go func(acc accepter, d *DisguiseConfig) {
			defer wg.Done()
			s.acceptLoop(ctx, acc, d)
		}(acc, d)

		go func(acc accepter) {
			<-ctx.Done()
			acc.Close()
		}(acc)
	}

	<-ctx.Done()
	wg.Wait()
	return nil
}

func (s *Server) acceptLoop(ctx context.Context, acc accepter, d *DisguiseConfig) {
	for {
		conn, err := acc.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[%s] accept error: %v", d.Type, err)
			continue
		}
		go s.admitTunnelConn(ctx, conn, d)
	}
}

func (s *Server) currentEpoch() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.epoch
}

func (s *Server) admitTunnelConn(ctx context.Context, conn net.Conn, d *DisguiseConfig) {
	res, err := serverHandshake(conn, s.cfg.Token, s.currentEpoch)
	if err != nil {
		conn.Close()
		return
	}

	switch res.role {
	case roleControl:
		s.mu.Lock()
		if s.controlConn != nil {
			s.mu.Unlock()
			serverRefuse(conn, ackBusy)
			conn.Close()
			return
		}
		epoch, err := randomBytes(8)
		if err != nil {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.mu.Unlock()

		if err := serverAckControl(conn, epoch); err != nil {
			conn.Close()
			return
		}
		s.runControl(ctx, conn, epoch, d)

	case rolePool:
		if err := serverAckPool(conn); err != nil {
			conn.Close()
			return
		}
		s.admitPoolConn(conn)
	}
}

// runControl owns one control-channel generation: it publishes the
// connection, serialises writes to it (heartbeats and NEED_CONN signals share
// one writer goroutine so they never interleave on the wire), and reads
// whatever the client sends until the connection breaks — at which point
// every pool connection and mux session belonging to this epoch is torn down,
// because a client that will reconnect will do so with a fresh epoch anyway.
func (s *Server) runControl(ctx context.Context, conn net.Conn, epoch []byte, d *DisguiseConfig) {
	log.Printf("control channel established from %s via [%s]", conn.RemoteAddr(), d.Type)

	outCh := make(chan byte, 64)
	s.mu.Lock()
	s.controlConn = conn
	s.epoch = epoch
	s.ctrlOut = outCh
	s.curDisguise = d.Type
	s.mu.Unlock()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		ticker := time.NewTicker(s.cfg.Heartbeat.Duration)
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
		conn.SetReadDeadline(time.Now().Add(3 * s.cfg.Heartbeat.Duration))
		b, err := readByte(conn)
		if err != nil {
			log.Printf("control channel from %s ended: %v", conn.RemoteAddr(), err)
			break
		}
		if b == sigClose {
			log.Printf("control channel from %s closed by client", conn.RemoteAddr())
			break
		}
		if b == sigHeartbeat {
			// The client's heartbeat doubles as its own RTT probe — see
			// runOnce in client.go. Answering immediately, rather than on the
			// next tick, is what makes the RTT it measures meaningful.
			select {
			case outCh <- sigPong:
			case <-ctx.Done():
			}
		}
	}

	s.mu.Lock()
	if s.controlConn == conn {
		s.controlConn = nil
		s.epoch = nil
		s.curDisguise = ""
		close(s.ctrlOut)
		s.ctrlOut = nil
	}
	s.mu.Unlock()
	conn.Close()

	s.teardownEpoch(epoch)
}

// teardownEpoch closes every pool connection and mux session that presented
// this epoch. They are all now orphaned: the client will not use them again
// once it notices the control channel is gone, and holding them open would
// just leak file descriptors until the OS-level keepalive finally notices.
func (s *Server) teardownEpoch(epoch []byte) {
	drained := 0
drainLoop:
	for {
		select {
		case c := <-s.poolQueue:
			c.Close()
			drained++
		default:
			break drainLoop
		}
	}
	if drained > 0 {
		log.Printf("closed %d idle pool connections from the ended generation", drained)
	}

	s.sessMu.Lock()
	for _, ms := range s.sessions {
		ms.session.Close()
	}
	s.sessions = nil
	s.sessMu.Unlock()
}

func (s *Server) requestMore() {
	s.mu.RLock()
	ch := s.ctrlOut
	s.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- sigNeedConn:
	default:
		// A request is already queued; the client will catch up. Piling on
		// more would just make the control channel's write goroutine busier
		// without getting a connection open any sooner.
	}
}

func (s *Server) admitPoolConn(conn net.Conn) {
	switch s.cfg.Mode {
	case "tcp":
		select {
		case s.poolQueue <- conn:
			s.resource.broadcast()
		default:
			log.Printf("pool queue full, closing spare connection from %s", conn.RemoteAddr())
			conn.Close()
		}

	case "tcpmux":
		session, err := smux.Client(conn, muxConfig(s.cfg))
		if err != nil {
			log.Printf("failed to open mux session with %s: %v", conn.RemoteAddr(), err)
			conn.Close()
			return
		}
		ms := &muxSession{session: session}
		s.sessMu.Lock()
		s.sessions = append(s.sessions, ms)
		s.sessMu.Unlock()
		s.resource.broadcast()

	default:
		// Mode "udp" never reaches here — its pool arrives on a dedicated
		// UDP socket (udpcarrier.go), not as a rolePool claim on a
		// disguise's TCP listener. Closing defensively rather than leaking
		// the connection if one ever did.
		conn.Close()
	}
}

// obtainPoolConn returns one ready pool connection (TCP mode), asking the
// client for more if none is immediately available and waiting up to ctx's
// deadline for one to show up.
func (s *Server) obtainPoolConn(ctx context.Context) net.Conn {
	for {
		select {
		case c := <-s.poolQueue:
			return c
		default:
		}
		s.requestMore()
		select {
		case c := <-s.poolQueue:
			return c
		case <-s.resource.wait():
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return nil
		}
	}
}

// obtainSession returns a mux session with spare stream capacity, asking the
// client to open another one if every known session is full.
func (s *Server) obtainSession(ctx context.Context) *muxSession {
	for {
		if ms := s.pickSession(); ms != nil {
			return ms
		}
		s.requestMore()
		select {
		case <-s.resource.wait():
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Server) pickSession() *muxSession {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()

	live := s.sessions[:0]
	var best *muxSession
	for _, ms := range s.sessions {
		if ms.session.IsClosed() {
			continue
		}
		live = append(live, ms)
		if int(atomic.LoadInt32(&ms.streams)) < s.cfg.MaxStreamsPerSession {
			if best == nil || atomic.LoadInt32(&ms.streams) < atomic.LoadInt32(&best.streams) {
				best = ms
			}
		}
	}
	s.sessions = live
	return best
}

func (s *Server) runPortListener(ctx context.Context, pm PortMap) {
	listener, err := net.Listen("tcp", pm.Listen)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", pm.Listen, err)
	}
	defer listener.Close()
	log.Printf("forwarding %s -> (client dials) %s", listener.Addr(), pm.Target)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("accept error on %s: %v", pm.Listen, err)
			continue
		}
		tuneConn(conn, true, s.cfg.KeepAlive.Duration, s.cfg.RecvBuf, s.cfg.SendBuf)
		go s.handleLocalConn(ctx, conn, pm.Target)
	}
}

func (s *Server) handleLocalConn(ctx context.Context, local net.Conn, target string) {
	obtainCtx, cancel := context.WithTimeout(ctx, s.cfg.DialTimeout.Duration+5*time.Second)
	carrier, release, ok := s.obtainCarrier(obtainCtx)
	cancel()
	if !ok {
		local.Close()
		return
	}
	defer release()

	if err := writeString(carrier, target); err != nil {
		carrier.Close()
		local.Close()
		return
	}
	Pipe(local, carrier, s.cfg.BufferSize)
}

// obtainCarrier borrows one unit of tunnel capacity — a pool connection in
// tcp mode, a freshly opened stream on a session with spare room in tcpmux
// mode — the operation both TCP and UDP forwarding need before they can
// announce a target and start relaying. release must be called exactly once
// when the caller is done with the carrier (Pipe/relay loop closing it is
// not enough by itself in tcpmux mode, which also has to give back the
// session's stream-count slot).
func (s *Server) obtainCarrier(ctx context.Context) (carrier net.Conn, release func(), ok bool) {
	switch s.cfg.Mode {
	case "tcp":
		pc := s.obtainPoolConn(ctx)
		if pc == nil {
			return nil, nil, false
		}
		return pc, func() {}, true

	case "tcpmux":
		ms := s.obtainSession(ctx)
		if ms == nil {
			return nil, nil, false
		}
		stream, err := ms.session.OpenStream()
		if err != nil {
			return nil, nil, false
		}
		atomic.AddInt32(&ms.streams, 1)
		return stream, func() { atomic.AddInt32(&ms.streams, -1) }, true

	default:
		return nil, nil, false
	}
}

// Stats returns a short snapshot for the periodic status log in main.go.
func (s *Server) Stats() string {
	s.mu.RLock()
	connected := s.controlConn != nil
	disguise := s.curDisguise
	s.mu.RUnlock()

	prefix := ""
	if connected {
		prefix = "[" + disguise + "] "
	}

	switch s.cfg.Mode {
	case "tcp":
		return prefix + statsLine(connected, "idle pool", len(s.poolQueue))
	default:
		s.sessMu.Lock()
		n := len(s.sessions)
		var streams int32
		for _, ms := range s.sessions {
			streams += atomic.LoadInt32(&ms.streams)
		}
		s.sessMu.Unlock()
		return prefix + statsLine(connected, "sessions", n) + statsLine(connected, "streams", int(streams))
	}
}

func statsLine(connected bool, label string, n int) string {
	if !connected {
		return "disconnected  "
	}
	return label + "=" + strconv.Itoa(n) + "  "
}
