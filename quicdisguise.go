package main

// Disguise type "quic": a TLS 1.3 session over UDP via quic-go, looking
// like ordinary HTTP/3 traffic on the wire and self-tuning its own
// congestion control — no manual window/interval knobs to get wrong,
// unlike kcpdisguise.go. Reuses the same cert_file/key_file/domain/
// insecure_skip_verify fields "wss" already has (see wss.go's
// loadOrGenerateCert) rather than inventing quic-specific ones, since the
// TLS setup question is identical either way.
//
// Same "one physical connection, one stream, wrapped as net.Conn" mapping
// as kcpdisguise.go: QUIC natively multiplexes streams over one
// connection, but this project already has that job covered by smux under
// Mode "tcpmux", so nothing here needs QUIC's own multiplexing — each
// dialed/accepted QUIC connection carries exactly one stream, and that
// stream is what every existing handshake/pool/mux code sees.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"github.com/quic-go/quic-go"
)

// quicALPN only exists because QUIC's TLS layer requires some ALPN value to
// complete its handshake — it identifies nothing sensitive, since this
// project's own HMAC challenge/response (protocol.go) is what actually
// authenticates a peer, the same as on every other disguise.
const quicALPN = "rmtunnel"

func quicTLSConfigClient(d *DisguiseConfig) *tls.Config {
	return &tls.Config{
		ServerName:         d.Domain,
		InsecureSkipVerify: d.Insecure,
		NextProtos:         []string{quicALPN},
	}
}

// quicSessionConfig sizes QUIC's own flow-control windows and keepalive
// from the tunnel's existing knobs, rather than adding quic-specific ones
// — RecvBuf is already "how much this tunnel should have in flight" on
// every other transport, and KeepAlive is already "how often to prove this
// connection is still alive."
func quicSessionConfig(cfg *Config) *quic.Config {
	c := &quic.Config{
		KeepAlivePeriod: cfg.KeepAlive.Duration,
		MaxIdleTimeout:  3 * cfg.KeepAlive.Duration,
	}
	if cfg.RecvBuf > 0 {
		c.MaxStreamReceiveWindow = uint64(cfg.RecvBuf)
		c.MaxConnectionReceiveWindow = uint64(cfg.RecvBuf) * 4
	}
	return c
}

// quicConn adapts one QUIC stream, plus the connection it belongs to (QUIC
// puts LocalAddr/RemoteAddr on the connection, not the stream), to
// net.Conn.
type quicConn struct {
	*quic.Stream
	conn *quic.Conn
}

func (c *quicConn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *quicConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func dialQUIC(ctx context.Context, cfg *Config, d *DisguiseConfig) (net.Conn, error) {
	conn, err := quic.DialAddr(ctx, d.ServerAddr, quicTLSConfigClient(d), quicSessionConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("quic: dialing %s: %w", d.ServerAddr, err)
	}
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		conn.CloseWithError(0, "")
		return nil, fmt.Errorf("quic: opening stream: %w", err)
	}
	return &quicConn{Stream: stream, conn: conn}, nil
}

// quicAccepter decouples accepting new QUIC connections from accepting the
// first stream on each one: Accept() (the interface every disguise's
// listener implements) must never block on one specific peer, or a
// connection that never opens a stream would stall every pool/control
// claim behind it. A background loop keeps accepting connections and, for
// each, a separate goroutine waits for its stream and feeds the result
// into connCh — the same shape wssListener already uses for its own
// upgrade-then-deliver step.
type quicAccepter struct {
	ln     *quic.Listener
	connCh chan net.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

func startQUICListener(cfg *Config, d *DisguiseConfig) (accepter, error) {
	cert, err := loadOrGenerateCert(d)
	if err != nil {
		return nil, fmt.Errorf("quic: certificate: %w", err)
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{quicALPN},
	}
	ln, err := quic.ListenAddr(d.ListenAddr, tlsConf, quicSessionConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("quic: listening on %s: %w", d.ListenAddr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &quicAccepter{ln: ln, connCh: make(chan net.Conn, 64), ctx: ctx, cancel: cancel}
	go a.acceptConnections()
	return a, nil
}

func (a *quicAccepter) acceptConnections() {
	for {
		conn, err := a.ln.Accept(a.ctx)
		if err != nil {
			return
		}
		go a.acceptStream(conn)
	}
}

func (a *quicAccepter) acceptStream(conn *quic.Conn) {
	stream, err := conn.AcceptStream(a.ctx)
	if err != nil {
		conn.CloseWithError(0, "")
		return
	}
	select {
	case a.connCh <- &quicConn{Stream: stream, conn: conn}:
	case <-a.ctx.Done():
		conn.CloseWithError(0, "")
	}
}

func (a *quicAccepter) Accept() (net.Conn, error) {
	select {
	case c := <-a.connCh:
		return c, nil
	case <-a.ctx.Done():
		return nil, fmt.Errorf("quic listener closed")
	}
}

func (a *quicAccepter) Close() error {
	a.cancel()
	return a.ln.Close()
}
