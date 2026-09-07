package main

import (
	"context"
	"net"
	"time"
)

// tuneConn applies the performance knobs from Config to an already-open TCP
// connection, on either side of a dial or an accept. Every one of these is a
// plain net.TCPConn method available on every platform Go supports — nothing
// here reaches for a raw syscall, which is exactly what lets this project
// build and run natively on Windows for local testing and cross-compile
// cleanly for the Linux boxes it actually deploys to. See docs/TUNING.md for
// what an OS-specific extra (MSS clamping, SO_REUSEPORT) would look like and
// where to add it.
func tuneConn(conn net.Conn, nodelay bool, keepAlive time.Duration, rcvBuf, sndBuf int) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	tcpConn.SetNoDelay(nodelay)
	if keepAlive > 0 {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(keepAlive)
	} else {
		tcpConn.SetKeepAlive(false)
	}
	if rcvBuf > 0 {
		tcpConn.SetReadBuffer(rcvBuf)
	}
	if sndBuf > 0 {
		tcpConn.SetWriteBuffer(sndBuf)
	}
}

// tuneTunnelConn is tuneConn plus the MSS clamp (see mss_linux.go), applied
// only to connections that actually cross the WAN hop this tunnel carries —
// dialLocal's backend connections stay on tuneConn alone, since clamping the
// MSS on the LAN-facing hop solves nothing and only shrinks its segments for
// no reason.
func tuneTunnelConn(conn net.Conn, cfg *Config) {
	tuneConn(conn, cfg.Nodelay, cfg.KeepAlive.Duration, cfg.RecvBuf, cfg.SendBuf)
	if tcpConn, ok := conn.(*net.TCPConn); ok && cfg.MSS > 0 {
		setMSS(tcpConn, cfg.MSS)
	}
}

// dialLocal opens the connection to the real backend a pool connection or mux
// stream was told to reach.
func dialLocal(ctx context.Context, cfg *Config, target string) (net.Conn, error) {
	d := net.Dialer{Timeout: cfg.DialTimeout.Duration}
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	tuneConn(conn, true, cfg.KeepAlive.Duration, cfg.RecvBuf, cfg.SendBuf)
	return conn, nil
}
