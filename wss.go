package main

// The "wss" disguise is the strongest one this project offers against
// today's filtering in Iran: the connection is an ordinary TLS handshake
// followed by an ordinary WebSocket upgrade, on whatever port you point it
// at (443 is the obvious choice — it is never itself the thing that gets
// blocked). Anything that hits the listener on a path other than the
// configured one gets a normal HTTP response (DecoyRoot, or a small built-in
// placeholder page) instead of anything tunnel-shaped, so a manual check or
// an automated probe that just requests the URL sees a website.
//
// What this does NOT do: present a TLS ClientHello that fingerprints as a
// real browser's (that needs a uTLS-style fingerprint library — see
// docs/CENSORSHIP.md), or use a certificate a real CA issued (a self-signed
// one works and is what this generates by default, but is itself
// distinguishable on close inspection — pointing cert_file/key_file at a
// real Let's Encrypt certificate for a domain you control is what actually
// makes this indistinguishable from a real site to more than casual
// inspection).

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

const defaultDecoyHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Coming soon</title></head>
<body style="font-family:sans-serif;max-width:40em;margin:4em auto">
<h1>Coming soon</h1><p>This site is being set up.</p></body></html>`

// wsConn adapts a message-framed *websocket.Conn to the byte-stream net.Conn
// interface the rest of this project (the HMAC handshake, smux, the raw pipe
// loop) expects. Every Write becomes exactly one binary WebSocket message;
// Read drains one message at a time into the caller's buffer, pulling the
// next one only once the current one is exhausted.
type wsConn struct {
	ws      *websocket.Conn
	pending []byte
}

func (c *wsConn) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		mt, data, err := c.ws.ReadMessage()
		if err != nil {
			return 0, err
		}
		if mt != websocket.BinaryMessage {
			continue // ignore stray text/control frames, keep reading
		}
		c.pending = data
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *wsConn) Write(p []byte) (int, error) {
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error                       { return c.ws.Close() }
func (c *wsConn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }
func (c *wsConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

// --- client side ------------------------------------------------------

func wssDial(ctx context.Context, full *Config, cfg *DisguiseConfig, timeout time.Duration) (net.Conn, error) {
	netDialer := &net.Dialer{Timeout: timeout}
	dialer := websocket.Dialer{
		HandshakeTimeout: timeout,
		TLSClientConfig: &tls.Config{
			ServerName:         cfg.Domain, // SNI, independent of the IP we actually dial
			InsecureSkipVerify: cfg.Insecure,
		},
		// gorilla dials the raw TCP connection internally, which is exactly
		// the connection recv_buf/send_buf/nodelay/MSS need to land on — a
		// socket left at OS defaults caps throughput hard on any link with
		// real RTT, regardless of how big the tunnel's own config says the
		// buffers should be. Without this hook there is no way to reach that
		// connection at all.
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := netDialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			tuneTunnelConn(conn, full)
			return conn, nil
		},
	}
	u := url.URL{Scheme: "wss", Host: cfg.ServerAddr, Path: cfg.Path}
	header := http.Header{}
	if cfg.Domain != "" {
		header.Set("Host", cfg.Domain)
	}
	ws, _, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return nil, err
	}
	return &wsConn{ws: ws}, nil
}

// --- server side ------------------------------------------------------

// wssListener bridges an HTTP+WebSocket server to the plain Accept-loop
// shape the rest of the server code uses for every disguise.
type wssListener struct {
	connCh chan net.Conn
	srv    *http.Server
	ln     net.Listener
}

// tunedListener wraps a net.Listener so every accepted connection gets
// recv_buf/send_buf/nodelay/MSS applied before anything (TLS included) reads
// or writes to it — see the matching comment in wssDial for why this has to
// happen at the raw-accept layer, not after http.Server has already taken
// the connection.
type tunedListener struct {
	net.Listener
	cfg *Config
}

func (l *tunedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tuneTunnelConn(conn, l.cfg)
	return conn, nil
}

func startWSSListener(full *Config, cfg *DisguiseConfig) (*wssListener, error) {
	cert, err := loadOrGenerateCert(cfg)
	if err != nil {
		return nil, fmt.Errorf("wss: certificate: %w", err)
	}

	rawLn, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, err
	}
	ln := &tunedListener{Listener: rawLn, cfg: full}

	l := &wssListener{connCh: make(chan net.Conn, 64), ln: ln}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.Path, func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		select {
		case l.connCh <- &wsConn{ws: ws}:
		default:
			ws.Close()
		}
	})
	mux.HandleFunc("/", decoyHandler(cfg.DecoyRoot))

	l.srv = &http.Server{
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
	}
	go l.srv.ServeTLS(ln, "", "") // certs come from TLSConfig, not files

	return l, nil
}

func (l *wssListener) Accept() (net.Conn, error) {
	conn, ok := <-l.connCh
	if !ok {
		return nil, fmt.Errorf("wss listener closed")
	}
	return conn, nil
}

func (l *wssListener) Close() error {
	l.srv.Close()
	return l.ln.Close()
}

func decoyHandler(root string) http.HandlerFunc {
	if root != "" {
		return http.FileServer(http.Dir(root)).ServeHTTP
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(defaultDecoyHTML))
	}
}

func loadOrGenerateCert(cfg *DisguiseConfig) (tls.Certificate, error) {
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		return tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	}
	return generateSelfSignedCert(cfg.Domain)
}

func generateSelfSignedCert(domain string) (tls.Certificate, error) {
	if domain == "" {
		domain = "localhost"
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: domain},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{domain},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}
