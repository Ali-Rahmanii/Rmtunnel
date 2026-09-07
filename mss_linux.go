//go:build linux

package main

import (
	"net"
	"syscall"
)

// setMSS clamps the TCP maximum segment size on a Linux socket. Useful when
// this tunnel itself runs inside something else with a reduced MTU (a VPN,
// another tunnel, GRE) — without this, the kernel negotiates an MSS based on
// the interface MTU it can see, which is too big for the path's real ceiling,
// and every oversized segment gets fragmented (or silently dropped, if some
// hop on the path blackholes fragments — the classic "SSH connects but hangs
// on any real transfer" symptom).
func setMSS(conn *net.TCPConn, mss int) error {
	if mss <= 0 {
		return nil
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	err = raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, mss)
	})
	if err != nil {
		return err
	}
	return sockErr
}
