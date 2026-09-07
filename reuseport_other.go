//go:build !linux

package main

import "net"

// listenTCP ignores reusePort outside Linux — SO_REUSEPORT's semantics
// (load-spreading across listeners, not just "allow rebind") are a Linux
// thing; other platforms either lack it or behave differently enough that
// pretending to support it here would be misleading. See reuseport_linux.go.
func listenTCP(addr string, reusePort bool) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
