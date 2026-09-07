package main

// rmtunnel bench measures what actually matters for picking a config
// preset — CPU, RAM, and the real RTT/throughput between the two boxes —
// instead of asking you to guess. Run `bench server` on one box and
// `bench client` on the other; the client prints a recommended tier
// (light/medium/heavy/insane) and the config block to go with it.
//
// This speaks its own tiny protocol, deliberately separate from the tunnel
// protocol in protocol.go: a benchmark has no pool, no mux, nothing to
// authenticate beyond "do both ends know the token" — reusing the HMAC
// challenge from protocol.go and nothing else keeps it that way.

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	benchPing byte = 0x01
	benchDown byte = 0x02
	benchUp   byte = 0x03

	benchTestDuration = 4 * time.Second
	benchPings        = 10
)

func runBenchServer(addr, token string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Printf("bench server listening on %s (port %s) — on the other box run:\n  rmtunnel bench client <this-box-ip>:%s %s\n",
		addr, portOf(addr), portOf(addr), token)
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go func() {
			if err := serveBenchConn(conn, token); err != nil {
				fmt.Fprintf(os.Stderr, "bench: connection from %s: %v\n", conn.RemoteAddr(), err)
			}
		}()
	}
}

func portOf(addr string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr
	}
	return addr[i+1:]
}

func serveBenchConn(conn net.Conn, token string) error {
	defer conn.Close()
	tuneConn(conn, true, 15*time.Second, 4*1024*1024, 4*1024*1024)

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	challenge, err := randomBytes(16)
	if err != nil {
		return err
	}
	if _, err := conn.Write(challenge); err != nil {
		return err
	}
	var tag [32]byte
	if _, err := io.ReadFull(conn, tag[:]); err != nil {
		return err
	}
	want := hmacTag(token, challenge)
	ok := len(tag) == len(want)
	for i := range want {
		ok = ok && tag[i] == want[i]
	}
	if !ok {
		writeByte(conn, 0)
		return fmt.Errorf("bad token")
	}
	writeByte(conn, 1)
	conn.SetDeadline(time.Time{})

	buf := make([]byte, 64*1024)
	for {
		cmd, err := readByte(conn)
		if err != nil {
			return nil // client disconnected — a normal end of the session
		}
		switch cmd {
		case benchPing:
			if err := writeByte(conn, benchPing); err != nil {
				return err
			}

		case benchDown:
			secs, err := readUint32(conn)
			if err != nil {
				return err
			}
			deadline := time.Now().Add(time.Duration(secs) * time.Second)
			for time.Now().Before(deadline) {
				conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				if _, err := conn.Write(buf); err != nil {
					// The client stops reading the instant its own timer
					// fires, which — on a link fast enough to have filled
					// the socket buffers by then — means whatever write is
					// in flight at that exact moment blocks until this
					// deadline trips. That is the download phase ending
					// exactly as designed, not a broken connection, so this
					// falls through to the next command rather than closing
					// the session.
					break
				}
			}
			conn.SetWriteDeadline(time.Time{})

		case benchUp:
			secs, err := readUint32(conn)
			if err != nil {
				return err
			}
			conn.SetReadDeadline(time.Now().Add(time.Duration(secs)*time.Second + 3*time.Second))
			var total uint64
			readDeadline := time.Now().Add(time.Duration(secs) * time.Second)
			for time.Now().Before(readDeadline) {
				n, err := conn.Read(buf)
				total += uint64(n)
				if err != nil {
					break
				}
			}
			conn.SetReadDeadline(time.Time{})
			var out [8]byte
			binary.BigEndian.PutUint64(out[:], total)
			if _, err := conn.Write(out[:]); err != nil {
				return err
			}

		default:
			return fmt.Errorf("unknown bench command %d", cmd)
		}
	}
}

func readUint32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

func writeUint32(w io.Writer, v uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, err := w.Write(b[:])
	return err
}

type benchResult struct {
	minRTT, avgRTT, maxRTT time.Duration
	downMbps, upMbps       float64
}

func runBenchClient(addr, token string) error {
	res, err := measureLink(addr, token)
	if err != nil {
		return err
	}
	printRecommendation(res)
	return nil
}

// measureLink runs the full RTT + throughput test against a bench server and
// returns the raw result — used both by the `bench client` CLI command and
// by the tunnel-creation wizard, which turns the result directly into config
// values instead of just printing them.
func measureLink(addr, token string) (benchResult, error) {
	fmt.Printf("connecting to %s...\n", addr)
	conn, err := net.DialTimeout("tcp", addr, 8*time.Second)
	if err != nil {
		return benchResult{}, err
	}
	defer conn.Close()
	tuneConn(conn, true, 15*time.Second, 4*1024*1024, 4*1024*1024)

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	var challenge [16]byte
	if _, err := io.ReadFull(conn, challenge[:]); err != nil {
		return benchResult{}, err
	}
	if _, err := conn.Write(hmacTag(token, challenge[:])); err != nil {
		return benchResult{}, err
	}
	ack, err := readByte(conn)
	if err != nil {
		return benchResult{}, err
	}
	if ack != 1 {
		return benchResult{}, fmt.Errorf("server rejected the token")
	}
	conn.SetDeadline(time.Time{})
	fmt.Println("connected. running tests...")

	var res benchResult

	// RTT
	rtts := make([]time.Duration, 0, benchPings)
	for i := 0; i < benchPings; i++ {
		start := time.Now()
		if err := writeByte(conn, benchPing); err != nil {
			return benchResult{}, err
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := readByte(conn); err != nil {
			return benchResult{}, err
		}
		rtts = append(rtts, time.Since(start))
	}
	conn.SetReadDeadline(time.Time{})
	res.minRTT, res.avgRTT, res.maxRTT = summarizeRTT(rtts)
	fmt.Printf("  RTT: min=%s avg=%s max=%s\n", res.minRTT.Round(time.Millisecond), res.avgRTT.Round(time.Millisecond), res.maxRTT.Round(time.Millisecond))

	// Download
	fmt.Printf("  measuring download for %s...\n", benchTestDuration)
	if err := writeByte(conn, benchDown); err != nil {
		return benchResult{}, err
	}
	if err := writeUint32(conn, uint32(benchTestDuration.Seconds())); err != nil {
		return benchResult{}, err
	}
	res.downMbps, err = measureDownload(conn, benchTestDuration)
	if err != nil {
		return benchResult{}, err
	}
	fmt.Printf("  download: %.1f Mbps\n", res.downMbps)

	// The server's write loop is bounded by its own wall clock, not by how
	// much the client has read, so a few last KB it was mid-write on when its
	// timer expired can still be in flight. Without draining those first,
	// they land at the front of the next phase's read and get mistaken for
	// its result — this is exactly what made the upload figure read as 0
	// during testing. Read-until-quiet resyncs the stream before that phase
	// starts.
	drainQuiet(conn, 300*time.Millisecond)

	// Upload
	fmt.Printf("  measuring upload for %s...\n", benchTestDuration)
	if err := writeByte(conn, benchUp); err != nil {
		return benchResult{}, err
	}
	if err := writeUint32(conn, uint32(benchTestDuration.Seconds())); err != nil {
		return benchResult{}, err
	}
	res.upMbps, err = measureUpload(conn, benchTestDuration)
	if err != nil {
		return benchResult{}, err
	}
	fmt.Printf("  upload: %.1f Mbps\n", res.upMbps)

	return res, nil
}

func summarizeRTT(rtts []time.Duration) (min, avg, max time.Duration) {
	if len(rtts) == 0 {
		return 0, 0, 0
	}
	min, max = rtts[0], rtts[0]
	var sum time.Duration
	for _, d := range rtts {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
		sum += d
	}
	return min, sum / time.Duration(len(rtts)), max
}

// drainQuiet reads and discards whatever arrives until quiet elapses with
// nothing new — i.e. until the stream has genuinely gone silent, not just
// until some fixed amount of time has passed.
func drainQuiet(conn net.Conn, quiet time.Duration) {
	buf := make([]byte, 64*1024)
	for {
		conn.SetReadDeadline(time.Now().Add(quiet))
		if _, err := conn.Read(buf); err != nil {
			break
		}
	}
	conn.SetReadDeadline(time.Time{})
}

func measureDownload(conn net.Conn, dur time.Duration) (float64, error) {
	buf := make([]byte, 64*1024)
	var total int64
	conn.SetReadDeadline(time.Now().Add(dur + 2*time.Second))
	start := time.Now()
	deadline := start.Add(dur)
	for time.Now().Before(deadline) {
		n, err := conn.Read(buf)
		total += int64(n)
		if err != nil {
			break
		}
	}
	conn.SetReadDeadline(time.Time{})
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return 0, nil
	}
	return float64(total) * 8 / elapsed / 1e6, nil
}

func measureUpload(conn net.Conn, dur time.Duration) (float64, error) {
	buf := make([]byte, 64*1024)
	deadline := time.Now().Add(dur)
	conn.SetWriteDeadline(time.Now().Add(dur + 5*time.Second))
	for time.Now().Before(deadline) {
		if _, err := conn.Write(buf); err != nil {
			break
		}
	}
	conn.SetWriteDeadline(time.Time{})

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var out [8]byte
	if _, err := io.ReadFull(conn, out[:]); err != nil {
		return 0, err
	}
	conn.SetReadDeadline(time.Time{})
	total := binary.BigEndian.Uint64(out[:])
	return float64(total) * 8 / dur.Seconds() / 1e6, nil
}

// --- hardware + recommendation ------------------------------------------

func localRAMMB() (int, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.Atoi(fields[1])
				if err == nil {
					return kb / 1024, true
				}
			}
		}
	}
	return 0, false
}

type tierPreset struct {
	name                                         string
	minIdle, maxIdle, maxStreamsPerSession       int
	bufferSize, recvBuf, sendBuf                 int
	muxRecvBuffer, muxStreamBuffer, muxFrameSize int
}

// muxFrameSize is capped at 65535 everywhere below: smux's frame header
// encodes it in 16 bits, so 65536 (a tempting round "64KB" value — and the
// one this project shipped with in these two tiers until a real test caught
// it) overflows and smux.VerifyConfig rejects the session outright. The
// failure mode is exactly the kind that reads as "the tunnel is broken" for
// the wrong reason: the control channel still connects fine (it isn't
// smux), so it looks up, while every mux session silently fails to open —
// see docs/TUNING.md and config.go's validate().
var tiers = []tierPreset{
	{"light", 2, 6, 16, 16 * 1024, 65536, 65536, 1 << 20, 256 << 10, 16 << 10},
	{"medium", 4, 16, 64, 32 * 1024, 256 << 10, 256 << 10, 4 << 20, 1 << 20, 32 << 10},
	{"heavy", 8, 32, 128, 64 * 1024, 1 << 20, 1 << 20, 8 << 20, 2 << 20, 65535},
	{"insane", 16, 64, 256, 128 * 1024, 4 << 20, 4 << 20, 16 << 20, 4 << 20, 65535},
}

func pickTier(cores, ramMB int, minMbps float64) tierPreset {
	switch {
	case cores >= 8 && minMbps >= 500:
		return tiers[3]
	case cores >= 4 && minMbps >= 100:
		return tiers[2]
	case cores >= 2 && minMbps >= 20:
		return tiers[1]
	default:
		return tiers[0]
	}
}

// bdpBytes is the bandwidth-delay product: how many bytes can be "in
// flight" on the link at once. A socket buffer smaller than this caps
// throughput below the link's real capacity no matter how fast the link
// actually is — this is usually the single biggest win on a high-RTT path
// (which the Iran<->Kharej route almost always is).
func bdpBytes(mbps float64, rtt time.Duration) int {
	return int(mbps * 1e6 / 8 * rtt.Seconds())
}

// tunedTier is a tier matched against a real (or manually entered)
// measurement, before and after the buffers that actually gate a single
// flow's throughput get floored at the link's bandwidth-delay product.
type tunedTier struct {
	base    tierPreset
	cores   int
	ramMB   int
	haveRAM bool
	minMbps float64
	rtt     time.Duration
	bdp     int
}

func computeTier(res benchResult) tunedTier {
	cores := runtime.NumCPU()
	ramMB, haveRAM := localRAMMB()
	minMbps := res.downMbps
	if res.upMbps < minMbps {
		minMbps = res.upMbps
	}
	return tunedTier{
		base:    pickTier(cores, ramMB, minMbps),
		cores:   cores,
		ramMB:   ramMB,
		haveRAM: haveRAM,
		minMbps: minMbps,
		rtt:     res.avgRTT,
		bdp:     bdpBytes(minMbps, res.avgRTT),
	}
}

// resolved returns the tier with recv_buf/send_buf (the raw socket window)
// and mux_stream_buffer (smux's equivalent for a single multiplexed stream)
// floored at the link's BDP. Undersized here is what silently caps a single
// flow to a fraction of the link's real capacity no matter how fast the
// link actually is — this was the concrete bug that made an early build of
// this project run far slower than its raw measured bandwidth (recv_buf/
// send_buf reached the socket fine, but the "wss" disguise's underlying
// connection was never tuned at all — see wss.go's NetDialContext/
// tunedListener — and mux_stream_buffer defaulted well under the BDP of any
// non-trivial-RTT link regardless of disguise).
func (t tunedTier) resolved() tierPreset {
	out := t.base
	if t.bdp > out.recvBuf {
		out.recvBuf = t.bdp
		out.sendBuf = t.bdp
	}
	if t.bdp > out.muxStreamBuffer {
		out.muxStreamBuffer = t.bdp
	}
	// The session-wide receive cap has to hold several streams' worth of
	// in-flight data at once, not just one.
	if min := out.muxStreamBuffer * 4; out.muxRecvBuffer < min {
		out.muxRecvBuffer = min
	}
	return out
}

func printRecommendation(res benchResult) {
	t := computeTier(res)
	tier := t.resolved()

	fmt.Println()
	fmt.Println("--- hardware (this box) ---")
	fmt.Printf("  CPU cores: %d\n", t.cores)
	if t.haveRAM {
		fmt.Printf("  RAM: %d MB\n", t.ramMB)
	} else {
		fmt.Println("  RAM: unknown (only read on Linux — run this on the actual Linux server for a full picture)")
	}

	fmt.Println()
	fmt.Printf("--- recommended tier: %s ---\n", t.base.name)
	fmt.Printf("  based on: %d cores, %.0f Mbps (weaker direction), %s avg RTT\n", t.cores, t.minMbps, t.rtt.Round(time.Millisecond))
	if tier.recvBuf > t.base.recvBuf {
		fmt.Printf("  recv_buf/send_buf/mux_stream_buffer raised above the tier default to match this link's bandwidth-delay product (%d KB)\n", t.bdp/1024)
	}
	fmt.Println()
	fmt.Println("paste this into your server.toml / client.toml:")
	fmt.Println()
	printTierTOML(tier)
	fmt.Println(dim("reminder: recv_buf/send_buf this large also need net.core.rmem_max/wmem_max raised on Linux — see \"Tune Server\" in the menu, or docs/TUNING.md."))
}

func printTierTOML(tier tierPreset) {
	fmt.Printf("min_idle = %d\n", tier.minIdle)
	fmt.Printf("max_idle = %d\n", tier.maxIdle)
	fmt.Printf("buffer_size = %d\n", tier.bufferSize)
	fmt.Printf("recv_buf = %d\n", tier.recvBuf)
	fmt.Printf("send_buf = %d\n", tier.sendBuf)
	fmt.Printf("max_streams_per_session = %d\n", tier.maxStreamsPerSession)
	fmt.Printf("mux_frame_size = %d\n", tier.muxFrameSize)
	fmt.Printf("mux_recv_buffer = %d\n", tier.muxRecvBuffer)
	fmt.Printf("mux_stream_buffer = %d\n", tier.muxStreamBuffer)
}

func runBench(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bench server <listen_addr> <token> | bench client <server_addr> <token>")
	}
	switch args[0] {
	case "server":
		if len(args) != 3 {
			return fmt.Errorf("usage: bench server <listen_addr> <token>")
		}
		return runBenchServer(args[1], args[2])
	case "client":
		if len(args) != 3 {
			return fmt.Errorf("usage: bench client <server_addr> <token>")
		}
		return runBenchClient(args[1], args[2])
	default:
		return fmt.Errorf("unknown bench subcommand %q", args[0])
	}
}
