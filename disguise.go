package main

import (
	"context"
	"fmt"
	"net"
)

// accepter is the minimum any disguise's server-side listener has to
// provide. "plain" and "noise" are backed by a real net.Listener; "wss" is
// backed by an HTTP server's upgrade handler feeding a channel — both look
// the same from here, which is the point: admitTunnelConn in server.go never
// has to know which one produced the connection it was just handed.
type accepter interface {
	Accept() (net.Conn, error)
	Close() error
}

func startDisguiseListener(cfg *Config, d *DisguiseConfig) (accepter, error) {
	switch d.Type {
	case "plain", "noise":
		ln, err := listenTCP(d.ListenAddr, cfg.ReusePort)
		if err != nil {
			return nil, err
		}
		return &plainAccepter{ln: ln, cfg: cfg, d: d}, nil
	case "wss":
		return startWSSListener(cfg, d)
	default:
		return nil, fmt.Errorf("unknown disguise type %q", d.Type)
	}
}

// plainAccepter tunes every accepted connection and, for "noise", completes
// the Noise handshake before returning it — a peer that fails the handshake
// costs one failed Accept() cycle, not a dead listener, so a stray scanner
// probing the port never disrupts real clients behind it.
type plainAccepter struct {
	ln  net.Listener
	cfg *Config
	d   *DisguiseConfig
}

func (a *plainAccepter) Accept() (net.Conn, error) {
	for {
		conn, err := a.ln.Accept()
		if err != nil {
			return nil, err
		}
		tuneTunnelConn(conn, a.cfg)
		if a.d.Type != "noise" {
			return conn, nil
		}
		wrapped, err := wrapNoiseServer(conn, a.cfg.Token)
		if err != nil {
			conn.Close()
			continue
		}
		return wrapped, nil
	}
}

func (a *plainAccepter) Close() error { return a.ln.Close() }

// dialDisguise opens one connection to the server side of profile d,
// applying whatever that disguise type requires (TCP tuning, a Noise
// handshake, or a full TLS+WebSocket upgrade) before handing it back.
func dialDisguise(ctx context.Context, cfg *Config, d *DisguiseConfig) (net.Conn, error) {
	switch d.Type {
	case "wss":
		return wssDial(ctx, cfg, d, cfg.DialTimeout.Duration)

	case "plain", "noise":
		dialer := net.Dialer{Timeout: cfg.DialTimeout.Duration}
		conn, err := dialer.DialContext(ctx, "tcp", d.ServerAddr)
		if err != nil {
			return nil, err
		}
		tuneTunnelConn(conn, cfg)
		if d.Type != "noise" {
			return conn, nil
		}
		wrapped, err := wrapNoiseClient(conn, cfg.Token)
		if err != nil {
			conn.Close()
			return nil, err
		}
		return wrapped, nil

	default:
		return nil, fmt.Errorf("unknown disguise type %q", d.Type)
	}
}
