package main

// UDP forwarding piggybacks on the same tunnel capacity (pool connections or
// mux streams) TCP forwarding uses — there is no separate UDP transport.
// When a forwarded port has udp=true, the server also listens for datagrams
// on that port. The first datagram from a given source address borrows one
// tunnel carrier exactly the way a new TCP connection does, announces it
// with a "udp:"-prefixed target instead of a plain one, and from then on
// every datagram in either direction is relayed as one writeDatagram frame —
// preserving packet boundaries a raw byte-stream Pipe would not. Because UDP
// has no close, a session with no traffic for udpIdleTimeout is torn down to
// free the carrier and the OS socket table entry.

import (
	"context"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	udpTargetPrefix = "udp:"
	udpIdleTimeout  = 90 * time.Second
	udpReapInterval = 30 * time.Second
	maxUDPDatagram  = 65507
)

// --- server side ------------------------------------------------------

type udpSession struct {
	carrier    net.Conn
	release    func()
	lastActive int64 // unix nano, atomic
}

func (s *Server) runUDPListener(ctx context.Context, pm PortMap) {
	conn, err := net.ListenPacket("udp", pm.Listen)
	if err != nil {
		log.Printf("failed to listen for UDP on %s: %v", pm.Listen, err)
		return
	}
	uconn := conn.(*net.UDPConn)
	defer uconn.Close()
	log.Printf("forwarding UDP %s -> (client dials) %s", pm.Listen, pm.Target)

	go func() {
		<-ctx.Done()
		uconn.Close()
	}()

	var mu sync.Mutex
	sessions := make(map[string]*udpSession)

	go func() {
		ticker := time.NewTicker(udpReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now().UnixNano()
				mu.Lock()
				for key, sess := range sessions {
					if now-atomic.LoadInt64(&sess.lastActive) > int64(udpIdleTimeout) {
						delete(sessions, key)
						sess.carrier.Close()
						sess.release()
					}
				}
				mu.Unlock()
			}
		}
	}()

	buf := make([]byte, maxUDPDatagram)
	for {
		n, raddr, err := uconn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		key := raddr.String()

		mu.Lock()
		sess, ok := sessions[key]
		mu.Unlock()

		if !ok {
			obtainCtx, cancel := context.WithTimeout(ctx, s.cfg.DialTimeout.Duration+5*time.Second)
			carrier, release, got := s.obtainCarrier(obtainCtx)
			cancel()
			if !got {
				continue
			}
			if err := writeString(carrier, udpTargetPrefix+pm.Target); err != nil {
				carrier.Close()
				release()
				continue
			}
			sess = &udpSession{carrier: carrier, release: release}
			atomic.StoreInt64(&sess.lastActive, time.Now().UnixNano())
			mu.Lock()
			sessions[key] = sess
			mu.Unlock()

			go func(sess *udpSession, replyTo net.Addr, key string) {
				defer func() {
					mu.Lock()
					if sessions[key] == sess {
						delete(sessions, key)
					}
					mu.Unlock()
					sess.carrier.Close()
					sess.release()
				}()
				rbuf := make([]byte, maxUDPDatagram)
				for {
					n, err := readDatagram(sess.carrier, rbuf)
					if err != nil {
						return
					}
					atomic.StoreInt64(&sess.lastActive, time.Now().UnixNano())
					uconn.WriteTo(rbuf[:n], replyTo)
				}
			}(sess, raddr, key)
		}

		atomic.StoreInt64(&sess.lastActive, time.Now().UnixNano())
		if writeDatagram(sess.carrier, buf[:n]) != nil {
			// The carrier is broken; its reader goroutine will notice on its
			// own next read and tear the session down. Dropping this one
			// datagram is exactly what a real link would do under loss.
		}
	}
}

// --- client side ------------------------------------------------------

// handleUDPCarrier is the client-side counterpart to a server's UDP session:
// dial the local UDP target once, then relay datagrams in both directions
// for as long as either side keeps sending them.
func (c *Client) handleUDPCarrier(ctx context.Context, carrier net.Conn, targetAddr string) {
	defer carrier.Close()

	raddr, err := net.ResolveUDPAddr("udp", targetAddr)
	if err != nil {
		return
	}
	local, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return
	}
	defer local.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, maxUDPDatagram)
		for {
			local.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			n, err := local.Read(buf)
			if err != nil {
				carrier.Close()
				return
			}
			if writeDatagram(carrier, buf[:n]) != nil {
				return
			}
		}
	}()

	buf := make([]byte, maxUDPDatagram)
	for {
		carrier.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		n, err := readDatagram(carrier, buf)
		if err != nil {
			local.Close()
			break
		}
		if _, err := local.Write(buf[:n]); err != nil {
			break
		}
	}
	<-done
}

// isUDPTarget reports whether a target string announced on a pool
// connection/stream marks it as a UDP relay rather than a TCP forward, and
// returns the real target address with the marker stripped.
func isUDPTarget(target string) (addr string, isUDP bool) {
	if strings.HasPrefix(target, udpTargetPrefix) {
		return target[len(udpTargetPrefix):], true
	}
	return target, false
}
