package main

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// bufPool hands out reusable copy buffers so a busy tunnel does not churn the
// GC allocating a fresh 32 KB (or whatever buffer_size is configured) slice
// per direction per connection. Sized on first use to whatever buffer_size
// the config asks for; a mismatched size (config changed between calls, which
// only happens in tests) just allocates fresh rather than reusing a wrong-size
// slice.
var bufPool = sync.Pool{New: func() any { return make([]byte, 0) }}

func getBuf(size int) []byte {
	b := bufPool.Get().([]byte)
	if cap(b) < size {
		return make([]byte, size)
	}
	return b[:size]
}

func putBuf(b []byte) { bufPool.Put(b[:0]) } //nolint:staticcheck

var totalBytesTransferred int64

// TotalBytesTransferred is a running counter across every pipe this process
// has ever run, for the stats logger in main.go.
func TotalBytesTransferred() int64 { return atomic.LoadInt64(&totalBytesTransferred) }

// metricsBytesIn/Out split that same running total by direction relative to
// carrier — the tunnel-side connection, named that in both Pipe's callers
// (server.go's handleLocalConn, client.go's serveTarget) — rather than
// relative to Role, so both sides of a tunnel report the same physical
// bytes the same way: "in" is whatever arrived from the tunnel, "out" is
// whatever this box sent into it. See metrics.go, which snapshots these to
// disk for the "Tunnel Metrics" menu screen — a separate process from
// whichever tunnel is actually running, so it can't just read a live
// variable.
var (
	metricsBytesIn  int64
	metricsBytesOut int64
)

// pipeTrailingGrace bounds how long Pipe waits for a second direction to
// finish on its own once the first direction is done. Only matters when that
// first direction's destination can't be half-closed (see copyDirection) —
// a *smux.Stream, *kcp.UDPSession, or *quic.Stream have no CloseWrite, so the
// peer is never told "no more data coming" and can otherwise block on Read
// forever, leaking the stream and its local backend connection until the
// whole tunnel session drops. This grace period still lets a real trailing
// reply (a client that half-closed its write side but is still awaiting a
// response) land normally in the common case, without leaking indefinitely
// in the case where no reply is coming.
const pipeTrailingGrace = 30 * time.Second

// Pipe copies bytes both ways between local and carrier until one side is
// done, then closes both. Each direction half-closes its destination as
// soon as its source hits EOF (when the underlying type supports it, e.g.
// *net.TCPConn), so a client that finished sending but is still waiting on
// a reply is not cut off — only fully closed once both directions have
// actually finished, or pipeTrailingGrace passes, whichever comes first.
// carrier is always the tunnel-side connection — see metricsBytesIn/Out.
func Pipe(local, carrier net.Conn, bufSize int) {
	done := make(chan struct{}, 2)
	go func() { copyDirection(local, carrier, bufSize, &metricsBytesIn); done <- struct{}{} }()
	go func() { copyDirection(carrier, local, bufSize, &metricsBytesOut); done <- struct{}{} }()

	remaining := 2
	<-done
	remaining--

	select {
	case <-done:
		remaining--
	case <-time.After(pipeTrailingGrace):
	}

	local.Close()
	carrier.Close()

	// Drain whatever's left: if the grace period expired above, the closes
	// just now unblock the still-running direction's Read, so this returns
	// almost immediately rather than actually waiting out another timeout.
	for ; remaining > 0; remaining-- {
		<-done
	}
}

func copyDirection(dst net.Conn, src net.Conn, bufSize int, dirCounter *int64) {
	buf := getBuf(bufSize)
	defer putBuf(buf)
	n, _ := io.CopyBuffer(dst, src, buf)
	atomic.AddInt64(&totalBytesTransferred, n)
	atomic.AddInt64(dirCounter, n)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}
