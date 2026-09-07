package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func genToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// disguiseAnswer is what the wizard collects for one [[disguise]] entry
// before it's rendered to TOML.
type disguiseAnswer struct {
	Type     string
	Addr     string // listen_addr (server) or server_addr (client)
	Domain   string
	Path     string
	CertFile string
	KeyFile  string
	Insecure bool
}

func wizardServer() {
	sectionHeader("Build Iran Tunnel (Server)")

	fmt.Println(dim("This wizard builds the server config. The ports you open here"))
	fmt.Println(dim("are what end users actually connect to."))
	fmt.Println()

	name := askTunnelName("server")
	fmt.Println()

	token := readLineDefault("Security token (blank = generate one)", "")
	if token == "" {
		token = genToken()
		fmt.Println(green("Token generated: ") + bold(token))
	}
	fmt.Println(yellow("⚠ put this exact token in the client (Kharej) config too."))
	fmt.Println()

	mode := "tcpmux"
	if !confirm("Use tcpmux mode (multiplexed, recommended)?", true) {
		mode = "tcp"
	}
	fmt.Println()

	disguises := askDisguises(true)
	fmt.Println()

	ports := askPorts()
	fmt.Println()

	kharejHost := readLineDefault("Kharej box's address (optional, only used to offer a live benchmark next)", "")
	tier := askTierPreset(kharejHost)

	toml := renderServerTOML(token, mode, disguises, ports, tier)
	finishWizard("server", name, toml)
}

func wizardClient() {
	sectionHeader("Build Kharej Tunnel (Client)")

	fmt.Println(dim("This is the box the real backend (X-UI, a panel, WireGuard, ...) runs on"))
	fmt.Println(dim("or can reach."))
	fmt.Println()

	name := askTunnelName("client")
	fmt.Println()

	token := readLineDefault("Security token (must match the server exactly)", "")
	for token == "" {
		fmt.Println(red("token can't be empty."))
		token = readLineDefault("Security token", "")
	}
	fmt.Println()

	mode := "tcpmux"
	if !confirm("Use tcpmux mode (must match the server)?", true) {
		mode = "tcp"
	}
	fmt.Println()

	disguises := askDisguises(false)
	fmt.Println()

	tier := askTierPreset(hostOnly(disguises[0].Addr))

	toml := renderClientTOML(token, mode, disguises, tier)
	finishWizard("client", name, toml)
}

// askTunnelName asks for a short identifier used to name this tunnel's
// config file and systemd instance (rmtunnel-<role>@<name>) — required
// because a box can run more than one tunnel at once; see tunnels.go.
func askTunnelName(role string) string {
	for {
		name := readLineDefault("Tunnel name (letters, digits, - and _ only)", "main")
		if isValidTunnelName(name) {
			if _, err := os.Stat(tunnelConfigPath(role, name)); err == nil {
				if !confirm("a "+role+" tunnel named \""+name+"\" already exists — overwrite it?", false) {
					continue
				}
			}
			return name
		}
		fmt.Println(red("invalid name — use only letters, digits, - and _."))
	}
}

// askDisguises walks through the three disguise types, asking which are
// enabled and, for wss, its extra fields. isServer decides whether it asks
// for listen_addr or server_addr.
func askDisguises(isServer bool) []disguiseAnswer {
	fmt.Println(bold("Which anti-filtering methods should be enabled?"))
	fmt.Println(dim("Recommended: enable all three — the client switches between them on its own."))
	fmt.Println(dim("Full explanation of each: docs/CENSORSHIP.md"))
	fmt.Println()

	var out []disguiseAnswer

	if confirm("  wss (TLS+WebSocket, looks like an ordinary HTTPS site — strongest)", true) {
		d := disguiseAnswer{Type: "wss", Path: "/ws"}
		if isServer {
			port := readLineDefault("    wss listen port", "443")
			d.Addr = "0.0.0.0:" + port
			d.Domain = readLineDefault("    domain (enter it if you have a real cert, else leave blank)", "")
			d.CertFile = readLineDefault("    cert file path (blank = self-signed)", "")
			if d.CertFile != "" {
				d.KeyFile = readLineDefault("    key file path", "")
			}
		} else {
			ip := readLine("    Iran server's public address: ")
			port := readLineDefault("    server's wss port", "443")
			d.Addr = ip + ":" + port
			d.Domain = readLineDefault("    domain (exactly what you set on the server, or blank)", "")
			d.Insecure = confirm("    does the server use a self-signed cert?", true)
		}
		out = append(out, d)
	}

	if confirm("  noise (encrypted, no fixed protocol signature)", true) {
		d := disguiseAnswer{Type: "noise"}
		if isServer {
			port := readLineDefault("    noise listen port", "9001")
			d.Addr = "0.0.0.0:" + port
		} else {
			ip := readLine("    Iran server's public address: ")
			port := readLineDefault("    server's noise port", "9001")
			d.Addr = ip + ":" + port
		}
		out = append(out, d)
	}

	if confirm("  plain (raw, fastest but easiest to fingerprint)", true) {
		d := disguiseAnswer{Type: "plain"}
		if isServer {
			port := readLineDefault("    plain listen port", "9000")
			d.Addr = "0.0.0.0:" + port
		} else {
			ip := readLine("    Iran server's public address: ")
			port := readLineDefault("    server's plain port", "9000")
			d.Addr = ip + ":" + port
		}
		out = append(out, d)
	}

	for len(out) == 0 {
		fmt.Println(red("you need to enable at least one."))
		out = askDisguises(isServer)
	}
	return out
}

// askTierPreset sizes the buffers for this tunnel. A tier name alone is a
// guess; what actually determines whether a single flow gets the link's real
// throughput is whether recv_buf/send_buf and mux_stream_buffer are at least
// the link's bandwidth-delay product (bandwidth × RTT) — see bench.go's
// tunedTier.resolved(). suggestedHost, if non-empty, prefills the live-bench
// address (the client wizard already knows the server's address; the server
// wizard doesn't know the Kharej box's, so it's asked for there instead).
func askTierPreset(suggestedHost string) tierPreset {
	fmt.Println(bold("Performance sizing"))
	fmt.Println(dim("The single biggest factor is the link's bandwidth-delay product — a buffer"))
	fmt.Println(dim("smaller than bandwidth×RTT caps a single connection's speed no matter how"))
	fmt.Println(dim("fast the link actually is. Measuring beats guessing."))
	fmt.Println()
	fmt.Println(menuItem("1", "run a live benchmark against the other box now (most accurate)"))
	fmt.Println(menuItem("2", "I know the link's bandwidth and RTT — enter them"))
	fmt.Println(menuItem("3", "just pick a tier (light/medium/heavy/insane)"))

	switch readLineDefault("choice", "3") {
	case "1":
		return askLiveBenchTier(suggestedHost)
	case "2":
		return askManualBenchTier()
	default:
		return askStaticTier()
	}
}

func askStaticTier() tierPreset {
	fmt.Println()
	fmt.Println(menuItem("1", tiers[0].name+" — a small VPS, or a lot of concurrent low-bandwidth users"))
	fmt.Println(menuItem("2", tiers[1].name+" (default) — a typical 2-4 core VPS, moderate traffic"))
	fmt.Println(menuItem("3", tiers[2].name+" — 4-8 cores, sustained high throughput"))
	fmt.Println(menuItem("4", tiers[3].name+" — 8+ cores, a fast dedicated link"))
	switch readLineDefault("choice", "2") {
	case "1":
		return tiers[0]
	case "3":
		return tiers[2]
	case "4":
		return tiers[3]
	default:
		return tiers[1]
	}
}

func askLiveBenchTier(suggestedHost string) tierPreset {
	fmt.Println()
	fmt.Println(dim("On the OTHER box, run this first (in another terminal) and leave it running:"))
	fmt.Println(dim("  rmtunnel bench server 0.0.0.0:9999 <any-temporary-token>"))
	fmt.Println()
	addrDefault := ""
	if suggestedHost != "" {
		addrDefault = suggestedHost + ":9999"
	}
	addr := readLineDefault("that box's bench address (ip:port)", addrDefault)
	token := readLine("the temporary token you used there: ")
	res, err := measureLink(addr, token)
	if err != nil {
		fmt.Println(red("benchmark failed: " + err.Error()))
		fmt.Println(dim("falling back to picking a tier by hand."))
		return askStaticTier()
	}
	tier := computeTier(res).resolved()
	fmt.Println(green("measured — using these values:"))
	printTierTOML(tier)
	pressEnter()
	return tier
}

func askManualBenchTier() tierPreset {
	fmt.Println()
	mbpsStr := readLineDefault("link bandwidth in Mbps (the weaker of upload/download)", "100")
	rttStr := readLineDefault("link RTT in ms (ping between the two boxes)", "80")
	mbps := parseFloatDefault(mbpsStr, 100)
	rttMs := parseFloatDefault(rttStr, 80)
	res := benchResult{avgRTT: time.Duration(rttMs * float64(time.Millisecond)), downMbps: mbps, upMbps: mbps}
	tier := computeTier(res).resolved()
	fmt.Println(green("computed — using these values:"))
	printTierTOML(tier)
	pressEnter()
	return tier
}

func parseFloatDefault(s string, def float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

// hostOnly strips the port from "host:port", for turning a disguise's
// server_addr into a bench-address suggestion.
func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func renderDisguiseBlock(d disguiseAnswer, isServer bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[[disguise]]\n")
	fmt.Fprintf(&b, "type = %q\n", d.Type)
	fmt.Fprintf(&b, "enabled = true\n")
	if isServer {
		fmt.Fprintf(&b, "listen_addr = %q\n", d.Addr)
	} else {
		fmt.Fprintf(&b, "server_addr = %q\n", d.Addr)
	}
	if d.Type == "wss" {
		fmt.Fprintf(&b, "domain = %q\n", d.Domain)
		fmt.Fprintf(&b, "path = %q\n", "/ws")
		if isServer {
			fmt.Fprintf(&b, "cert_file = %q\n", d.CertFile)
			fmt.Fprintf(&b, "key_file = %q\n", d.KeyFile)
		} else {
			fmt.Fprintf(&b, "insecure_skip_verify = %v\n", d.Insecure)
		}
	}
	return b.String()
}

func renderServerTOML(token, mode string, disguises []disguiseAnswer, ports []PortMap, tier tierPreset) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# generated by the rmtunnel wizard — %s\n\n", RepoURL)
	fmt.Fprintf(&b, "mode = %q\ntoken = %q\n\n", mode, token)
	for _, d := range disguises {
		b.WriteString(renderDisguiseBlock(d, true))
		b.WriteString("\n")
	}
	for _, p := range ports {
		fmt.Fprintf(&b, "[[ports]]\nlisten = %q\ntarget = %q\nudp = %v\n\n", p.Listen, p.Target, p.UDP)
	}
	b.WriteString(renderTuningBlock(tier))
	return b.String()
}

func renderClientTOML(token, mode string, disguises []disguiseAnswer, tier tierPreset) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# generated by the rmtunnel wizard — %s\n\n", RepoURL)
	fmt.Fprintf(&b, "mode = %q\ntoken = %q\n\n", mode, token)
	for _, d := range disguises {
		b.WriteString(renderDisguiseBlock(d, false))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "retry_min = \"1s\"\nretry_max = \"30s\"\n")
	b.WriteString(renderTuningBlock(tier))
	return b.String()
}

func renderTuningBlock(t tierPreset) string {
	return fmt.Sprintf(`heartbeat = "5s"
nodelay = true
keepalive = "15s"
recv_buf = %d
send_buf = %d
dial_timeout = "8s"
buffer_size = %d
mss = 0
reuse_port = false

min_idle = %d
max_idle = %d
idle_grace = "20s"

max_streams_per_session = %d
mux_frame_size = %d
mux_recv_buffer = %d
mux_stream_buffer = %d
mux_keepalive = "10s"
`, t.recvBuf, t.sendBuf, t.bufferSize, t.minIdle, t.maxIdle, t.maxStreamsPerSession, t.muxFrameSize, t.muxRecvBuffer, t.muxStreamBuffer)
}

// finishWizard shows the generated config, saves it under this tunnel's
// name, and offers to install it as a templated systemd service instance —
// see tunnels.go for the naming/layout this relies on.
func finishWizard(role, name, tomlText string) {
	fmt.Println(bold(green("--- config generated ---")))
	fmt.Println(dim(tomlText))

	path := tunnelConfigPath(role, name)
	if runtime.GOOS != "linux" {
		wd, _ := os.Getwd()
		path = filepath.Join(wd, role+"-"+name+".toml")
	}
	path = readLineDefault("save path", path)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Println(red("failed to create directory: " + err.Error()))
		pressEnter()
		return
	}
	if err := os.WriteFile(path, []byte(tomlText), 0o600); err != nil {
		fmt.Println(red("failed to write file: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println(green("saved: " + path))

	if runtime.GOOS != "linux" {
		fmt.Println(dim("systemd service install is only available on Linux — copy this config to the real server."))
		pressEnter()
		return
	}

	if confirm("install and start this as a systemd service now?", true) {
		installTunnelService(role, name, path)
	}
	pressEnter()
}
