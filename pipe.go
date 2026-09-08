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

// Pipe copies bytes both ways between a and b until one side is done, then
// closes both. Each direction half-closes its destination as soon as its
// source hits EOF (when the underlying type supports it, e.g. *net.TCPConn),
// so a client that finished sending but is still waiting on a reply is not
// cut off — only fully closed once both directions have actually finished,
// or pipeTrailingGrace passes, whichever comes first.
func Pipe(a, b net.Conn, bufSize int) {
	done := make(chan struct{}, 2)
	go func() { copyDirection(b, a, bufSize); done <- struct{}{} }()
	go func() { copyDirection(a, b, bufSize); done <- struct{}{} }()

	remaining := 2
	<-done
	remaining--

	select {
	case <-done:
		remaining--
	case <-time.After(pipeTrailingGrace):
	}

	a.Close()
	b.Close()

	// Drain whatever's left: if the grace period expired above, the closes
	// just now unblock the still-running direction's Read, so this returns
	// almost immediately rather than actually waiting out another timeout.
	for ; remaining > 0; remaining-- {
		<-done
	}
}

func copyDirection(dst net.Conn, src net.Conn, bufSize int) {
	buf := getBuf(bufSize)
	defer putBuf(buf)
	n, _ := io.CopyBuffer(dst, src, buf)
	atomic.AddInt64(&totalBytesTransferred, n)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}
