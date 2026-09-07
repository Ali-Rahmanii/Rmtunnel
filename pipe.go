package main

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
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

// Pipe copies bytes both ways between a and b until one side is done, then
// closes both. Each direction half-closes its destination as soon as its
// source hits EOF (when the underlying type supports it, e.g. *net.TCPConn),
// so a client that finished sending but is still waiting on a reply is not
// cut off — only fully closed once both directions have actually finished.
// A naive io.Copy-then-Close-everything proxy drops that trailing reply.
func Pipe(a, b net.Conn, bufSize int) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyDirection(b, a, bufSize) }()
	go func() { defer wg.Done(); copyDirection(a, b, bufSize) }()
	wg.Wait()
	a.Close()
	b.Close()
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
