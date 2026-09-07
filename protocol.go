package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// --- wire constants -------------------------------------------------------

var magic = [4]byte{'R', 'M', 'T', '1'}

const (
	roleControl byte = 1
	rolePool    byte = 2
)

const (
	ackOK      byte = 1
	ackBusy    byte = 2 // a control channel is already established
	ackBadAuth byte = 3
)

// Control-channel signals. One byte, either direction.
const (
	sigHeartbeat byte = 0x10
	sigNeedConn  byte = 0x11 // server -> client: "open one more pool conn/session"
	sigClose     byte = 0x12 // graceful "I'm shutting down"
	sigPong      byte = 0x13 // reply to a heartbeat, carries no payload — see health.go
)

const handshakeTimeout = 10 * time.Second
const maxTargetLen = 512

// --- framing helpers -------------------------------------------------------

// writeString sends a length-prefixed UTF-8 string: 2-byte big-endian length
// followed by the bytes. Used for the one thing that needs a payload on this
// protocol — the backend target address handed to a pool connection/stream.
func writeString(w io.Writer, s string) error {
	if len(s) > maxTargetLen {
		return fmt.Errorf("string too long: %d bytes", len(s))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(s)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := io.WriteString(w, s)
	return err
}

func readString(r io.Reader) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	if n > maxTargetLen {
		return "", fmt.Errorf("string too long: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// writeDatagram/readDatagram use the same 2-byte length-prefix framing as
// writeString/readString, but sized for a full UDP payload (up to 65507
// bytes) rather than the short target-address strings maxTargetLen caps —
// see udp.go, which relays one UDP datagram per frame in each direction so
// packet boundaries survive the trip through a byte-stream tunnel.
func writeDatagram(w io.Writer, p []byte) error {
	if len(p) > 65535 {
		return fmt.Errorf("datagram too large: %d bytes", len(p))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(p)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(p)
	return err
}

func readDatagram(r io.Reader, buf []byte) (int, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n > len(buf) {
		return 0, fmt.Errorf("datagram too large for buffer: %d bytes", n)
	}
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return 0, err
	}
	return n, nil
}

func readByte(r io.Reader) (byte, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}

func writeByte(w io.Writer, b byte) error {
	_, err := w.Write([]byte{b})
	return err
}

// --- auth --------------------------------------------------------------
//
// The token never crosses the wire. Instead: the verifier sends a fresh
// random challenge, and the prover answers with HMAC-SHA256(token,
// challenge). A passive observer sees a random-looking 32-byte tag that is
// useless on any other connection, because every connection gets its own
// challenge. This is deliberately simple — it is not a substitute for the
// Noise-based stealth layer BackPack offers against active probing/DPI, just
// a step up from sending the token in the clear. See docs/TUNING.md for what
// a fuller stealth layer would add on top.

func hmacTag(token string, challenge []byte) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(challenge)
	return mac.Sum(nil)
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// --- server side handshake -------------------------------------------------

type admitted struct {
	role  byte
	epoch []byte // only meaningful for a pool connection: which control-channel generation it claims
}

// serverHandshake authenticates one freshly accepted connection and reports
// what it is asking to be. It never blocks past handshakeTimeout.
func serverHandshake(conn net.Conn, token string, currentEpoch func() []byte) (admitted, error) {
	conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	var hdr [5]byte // 4 magic + 1 role
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return admitted{}, fmt.Errorf("read header: %w", err)
	}
	if [4]byte(hdr[:4]) != magic {
		return admitted{}, errors.New("bad magic")
	}
	role := hdr[4]
	if role != roleControl && role != rolePool {
		return admitted{}, fmt.Errorf("unknown role %d", role)
	}

	challenge, err := randomBytes(16)
	if err != nil {
		return admitted{}, err
	}
	if _, err := conn.Write(challenge); err != nil {
		return admitted{}, err
	}

	var tag [32]byte
	if _, err := io.ReadFull(conn, tag[:]); err != nil {
		return admitted{}, fmt.Errorf("read auth tag: %w", err)
	}
	want := hmacTag(token, challenge)
	if subtle.ConstantTimeCompare(tag[:], want) != 1 {
		writeByte(conn, ackBadAuth)
		return admitted{}, errors.New("auth failed")
	}

	result := admitted{role: role}
	if role == rolePool {
		var epoch [8]byte
		if _, err := io.ReadFull(conn, epoch[:]); err != nil {
			return admitted{}, fmt.Errorf("read epoch: %w", err)
		}
		result.epoch = epoch[:]
		want := currentEpoch()
		if want == nil || subtle.ConstantTimeCompare(epoch[:], want) != 1 {
			writeByte(conn, ackBadAuth)
			return admitted{}, errors.New("stale or foreign pool epoch")
		}
	}
	return result, nil
}

// --- client side handshake -------------------------------------------------

// clientHandshakeControl authenticates as the control channel and, on
// success, returns the epoch the server wants every pool connection to
// present. The caller still owns the deadline it sets before/after.
func clientHandshakeControl(conn net.Conn, token string) ([]byte, error) {
	conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	if err := writeHeader(conn, roleControl); err != nil {
		return nil, err
	}
	var challenge [16]byte
	if _, err := io.ReadFull(conn, challenge[:]); err != nil {
		return nil, fmt.Errorf("read challenge: %w", err)
	}
	tag := hmacTag(token, challenge[:])
	if _, err := conn.Write(tag); err != nil {
		return nil, err
	}

	ack, err := readByte(conn)
	if err != nil {
		return nil, fmt.Errorf("read ack: %w", err)
	}
	switch ack {
	case ackOK:
		var epoch [8]byte
		if _, err := io.ReadFull(conn, epoch[:]); err != nil {
			return nil, fmt.Errorf("read epoch: %w", err)
		}
		return epoch[:], nil
	case ackBusy:
		return nil, errors.New("server already has an active control channel")
	case ackBadAuth:
		return nil, errors.New("token rejected by server")
	default:
		return nil, fmt.Errorf("unexpected ack %d", ack)
	}
}

// clientHandshakePool authenticates one pool connection against the epoch
// the control channel was given.
func clientHandshakePool(conn net.Conn, token string, epoch []byte) error {
	conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	if err := writeHeader(conn, rolePool); err != nil {
		return err
	}
	var challenge [16]byte
	if _, err := io.ReadFull(conn, challenge[:]); err != nil {
		return fmt.Errorf("read challenge: %w", err)
	}
	tag := hmacTag(token, challenge[:])
	if _, err := conn.Write(tag); err != nil {
		return err
	}
	if _, err := conn.Write(epoch); err != nil {
		return err
	}
	ack, err := readByte(conn)
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	if ack != ackOK {
		return fmt.Errorf("pool connection refused (code %d)", ack)
	}
	return nil
}

func writeHeader(conn net.Conn, role byte) error {
	buf := make([]byte, 5)
	copy(buf[:4], magic[:])
	buf[4] = role
	_, err := conn.Write(buf)
	return err
}

// --- server ack helpers ----------------------------------------------------

func serverAckControl(conn net.Conn, epoch []byte) error {
	if err := writeByte(conn, ackOK); err != nil {
		return err
	}
	_, err := conn.Write(epoch)
	return err
}

func serverRefuse(conn net.Conn, code byte) {
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	writeByte(conn, code)
}

func serverAckPool(conn net.Conn) error {
	return writeByte(conn, ackOK)
}
