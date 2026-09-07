//go:build !linux

package main

import "net"

// setMSS is a no-op outside Linux: TCP_MAXSEG is not exposed the same way
// (or at all) on every platform Go supports, and this project would rather
// build and run everywhere than fail on Windows for the sake of a knob that
// only matters on the Linux boxes it actually deploys to. See mss_linux.go.
func setMSS(conn *net.TCPConn, mss int) error { return nil }
