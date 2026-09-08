package main

// A forwarded port's target can name more than one backend, separated by
// "|" — "443=127.0.0.1:2096|127.0.0.1:2097" — each continuously health-
// checked with a plain TCP connect probe, load-balanced round-robin over
// whichever are currently reachable. Modeled on BackPack's own backend
// pool (internal/server/transport/backendpool.go, researched for this):
// probe every 10s, eject after 3 consecutive failures, fall back to trying
// the first configured backend anyway if every one currently looks down
// (an outright refusal helps nobody when the health check itself might be
// wrong).
//
// This lives entirely client-side (see client.go's serveTarget): the
// client is the box that can actually reach the backends to probe them —
// the server generally cannot, since a bare port's backend is
// 127.0.0.1-relative to the client's own machine. The server just forwards
// the whole "backend1|backend2" string unchanged; only the client ever
// parses or acts on the "|".
//
// UDP targets are a documented exception: BackPack's own doesn't health-
// check or balance those either (a UDP probe cannot straightforwardly
// prove "is this backend reachable" the way a TCP connect can), so a
// multi-backend UDP target just uses its first entry — see
// isUDPTarget/handleUDPCarrier in udp.go.

import (
	"net"
	"strings"
	"sync"
	"time"
)

const (
	backendProbeInterval = 10 * time.Second
	backendProbeTimeout  = 3 * time.Second
	backendFailThreshold = 3
)

type backendPool struct {
	backends []string

	mu      sync.Mutex
	healthy map[string]bool
	fails   map[string]int
	next    int
}

var (
	backendPoolsMu sync.Mutex
	backendPools   = map[string]*backendPool{} // keyed by the raw "b1|b2|..." target string
)

// getBackendPool returns the pool for this exact target spec, creating and
// starting its health-check loop on first use — shared across every flow
// to the same port, so repeated connections don't each start their own
// independent probing.
func getBackendPool(target string) *backendPool {
	backendPoolsMu.Lock()
	defer backendPoolsMu.Unlock()
	if p, ok := backendPools[target]; ok {
		return p
	}

	var backends []string
	for _, b := range strings.Split(target, "|") {
		if b = strings.TrimSpace(b); b != "" {
			backends = append(backends, b)
		}
	}
	p := &backendPool{
		backends: backends,
		healthy:  make(map[string]bool, len(backends)),
		fails:    make(map[string]int, len(backends)),
	}
	for _, b := range backends {
		p.healthy[b] = true // optimistic until the first probe says otherwise
	}
	backendPools[target] = p

	if len(backends) > 1 {
		go p.healthLoop()
	}
	return p
}

func (p *backendPool) healthLoop() {
	ticker := time.NewTicker(backendProbeInterval)
	defer ticker.Stop()
	for range ticker.C {
		for _, b := range p.backends {
			ok := probeTCP(b)
			p.mu.Lock()
			if ok {
				p.fails[b] = 0
				p.healthy[b] = true
			} else if p.fails[b]++; p.fails[b] >= backendFailThreshold {
				p.healthy[b] = false
			}
			p.mu.Unlock()
		}
	}
}

func probeTCP(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, backendProbeTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// pick returns the next backend to dial — round-robined over whichever
// currently look healthy, or the first configured one if the pool has
// never been asked before (no probe has run yet) or none look healthy
// right now.
func (p *backendPool) pick() string {
	if len(p.backends) <= 1 {
		if len(p.backends) == 0 {
			return ""
		}
		return p.backends[0]
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	var live []string
	for _, b := range p.backends {
		if p.healthy[b] {
			live = append(live, b)
		}
	}
	if len(live) == 0 {
		return p.backends[0]
	}
	b := live[p.next%len(live)]
	p.next++
	return b
}

// pickBackend is getBackendPool(target).pick() — the one call site
// serveTarget actually needs.
func pickBackend(target string) string {
	return getBackendPool(target).pick()
}
