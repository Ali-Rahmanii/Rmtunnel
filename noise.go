package main

// The "noise" disguise removes the one fixed, DPI-matchable thing the plain
// transport has: the 5-byte "RMT1"+role header that opens every connection.
// Wrapping the raw TCP connection in a Noise Protocol Framework channel
// before any of our own bytes go out means the very first byte on the wire
// is already ciphertext — there is no protocol signature left to match,
// only a stream that looks like uniform random data.
//
// Pattern: NN with a pre-shared key mixed into the first message (the
// "psk0" placement). NN needs no static keypair on either side — nothing to
// generate, store or leak — and the PSK, derived from the tunnel Token via
// HKDF, is what makes an eavesdropper unable to complete the handshake
// without already knowing the token: anyone who tries gets a handshake that
// fails to authenticate, and nothing about *why* it failed is visible from
// outside. A port scan or an active probe sees a TCP port that accepted a
// connection and then said nothing usable back.
//
// This defeats signature/plaintext matching. It does not by itself defeat
// traffic-shape analysis (record sizes, timing) — see docs/CENSORSHIP.md for
// what a fuller countermeasure (record padding) would add and why it was
// left out of this pass.

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
	"golang.org/x/crypto/hkdf"
)

const (
	noiseHandshakeTimeout = 15 * time.Second
	noiseMaxRecord        = 65535 - 16 // Noise message ceiling minus the AEAD tag
	noiseHKDFInfo         = "rmtunnel-noise-psk-v1"
)

var noiseSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

// derivePSK turns the shared tunnel token into a 32-byte pre-shared key. A
// plain SHA-256 of the token would work too, but HKDF keeps this key
// cryptographically separate from anything else ever derived from the same
// token (there is nothing else today, but it costs nothing to not share the
// well).
func derivePSK(token string) []byte {
	psk := make([]byte, 32)
	r := hkdf.New(sha256.New, []byte(token), nil, []byte(noiseHKDFInfo))
	io.ReadFull(r, psk) // hkdf.Read from a fresh SHA-256 reader cannot fail here
	return psk
}

func wrapNoiseClient(raw net.Conn, token string) (net.Conn, error) {
	return noiseHandshake(raw, token, true)
}

func wrapNoiseServer(raw net.Conn, token string) (net.Conn, error) {
	return noiseHandshake(raw, token, false)
}

func noiseHandshake(raw net.Conn, token string, initiator bool) (net.Conn, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           noiseSuite,
		Pattern:               noise.HandshakeNN,
		Initiator:             initiator,
		PresharedKey:          derivePSK(token),
		PresharedKeyPlacement: 0, // mixed into the first message ("psk0")
	})
	if err != nil {
		return nil, fmt.Errorf("noise: init: %w", err)
	}

	raw.SetDeadline(time.Now().Add(noiseHandshakeTimeout))
	defer raw.SetDeadline(time.Time{})

	var send, recv *noise.CipherState
	if initiator {
		msg, _, _, err := hs.WriteMessage(nil, nil)
		if err != nil {
			return nil, fmt.Errorf("noise: build message 1: %w", err)
		}
		if err := writeFrame(raw, msg); err != nil {
			return nil, err
		}
		in, err := readFrame(raw)
		if err != nil {
			return nil, err
		}
		// Wrong token (or a stranger entirely) fails right here.
		_, cs0, cs1, err := hs.ReadMessage(nil, in)
		if err != nil {
			return nil, fmt.Errorf("noise: handshake rejected: %w", err)
		}
		send, recv = cs0, cs1
	} else {
		in, err := readFrame(raw)
		if err != nil {
			return nil, err
		}
		if _, _, _, err := hs.ReadMessage(nil, in); err != nil {
			return nil, fmt.Errorf("noise: handshake rejected: %w", err)
		}
		msg, cs0, cs1, err := hs.WriteMessage(nil, nil)
		if err != nil {
			return nil, fmt.Errorf("noise: build message 2: %w", err)
		}
		if err := writeFrame(raw, msg); err != nil {
			return nil, err
		}
		send, recv = cs1, cs0
	}

	if send == nil || recv == nil {
		return nil, fmt.Errorf("noise: handshake did not complete")
	}
	return &noiseConn{Conn: raw, send: send, recv: recv}, nil
}

// noiseConn is the record layer over a completed handshake: every Write is
// one or more encrypted, length-prefixed records; every Read decrypts the
// next one on demand.
type noiseConn struct {
	net.Conn

	writeMu sync.Mutex
	send    *noise.CipherState

	readMu  sync.Mutex
	recv    *noise.CipherState
	pending []byte // decrypted bytes not yet handed to the caller
}

func (c *noiseConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > noiseMaxRecord {
			chunk = chunk[:noiseMaxRecord]
		}
		enc, err := c.send.Encrypt(nil, nil, chunk)
		if err != nil {
			return total, fmt.Errorf("noise: encrypt: %w", err)
		}
		if err := writeFrame(c.Conn, enc); err != nil {
			return total, err
		}
		total += len(chunk)
		p = p[len(chunk):]
	}
	return total, nil
}

func (c *noiseConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if len(c.pending) == 0 {
		frame, err := readFrame(c.Conn)
		if err != nil {
			return 0, err
		}
		plain, err := c.recv.Decrypt(nil, nil, frame)
		if err != nil {
			return 0, fmt.Errorf("noise: record failed to authenticate: %w", err)
		}
		c.pending = plain
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func writeFrame(w io.Writer, msg []byte) error {
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(msg)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(msg)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
