package main

// startDebugServer optionally exposes Go's own runtime profiler — off
// unless debug_pprof_addr is explicitly set in the config (see config.go),
// and even then only on whatever address is configured there, never opened
// automatically. This exists for exactly one situation: a box whose actual
// memory or CPU usage doesn't match what its config alone would predict,
// where guessing at the cause is a poor substitute for actually looking —
// set debug_pprof_addr to "127.0.0.1:6060" (never a public address; put it
// behind an SSH tunnel to reach it from elsewhere), restart, then:
//
//	go tool pprof http://127.0.0.1:6060/debug/pprof/heap
//	go tool pprof http://127.0.0.1:6060/debug/pprof/goroutine
//
// and unset it again once done — this is a diagnostic, not something to
// leave running.
import (
	"log"
	"net/http"
	_ "net/http/pprof"
)

func startDebugServer(addr string) {
	if addr == "" {
		return
	}
	log.Printf("debug pprof server listening on %s — NOT for production use, unset debug_pprof_addr when done", addr)
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Printf("debug pprof server failed: %v", err)
		}
	}()
}
