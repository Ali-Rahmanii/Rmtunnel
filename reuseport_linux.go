//go:build linux

package main

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// listenTCP opens a TCP listener, optionally with SO_REUSEPORT so several
// listeners (or, with GOMAXPROCS-style sharding, several accept loops in
// this same process) can share one port and let the kernel spread incoming
// connections across them — useful on a multi-core server taking a heavy
// rate of new connections, where a single accept loop can itself become the
// bottleneck.
func listenTCP(addr string, reusePort bool) (net.Listener, error) {
	if !reusePort {
		return net.Listen("tcp", addr)
	}
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var sockErr error
			err := c.Control(func(fd uintptr) {
				sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			})
			if err != nil {
				return err
			}
			return sockErr
		},
	}
	return lc.Listen(context.Background(), "tcp", addr)
}
