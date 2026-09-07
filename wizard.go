package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	var ports []PortMap
	fmt.Println(bold("Enter the ports you want forwarded."))
	fmt.Println(dim("Format: public_port=target_on_kharej_box  (e.g. 8080=127.0.0.1:8080)"))
	fmt.Println(dim("Blank line to finish."))
	for {
		line := readLine(fmt.Sprintf("port %d (blank=done): ", len(ports)+1))
		if line == "" {
			if len(ports) == 0 {
				fmt.Println(red("at least one port is required."))
				continue
			}
			break
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			fmt.Println(red("bad format. example: 8080=127.0.0.1:8080"))
			continue
		}
		ports = append(ports, PortMap{
			Listen: "0.0.0.0:" + strings.TrimSpace(parts[0]),
			Target: strings.TrimSpace(parts[1]),
		})
	}
	fmt.Println()

	tier := askTierPreset()

	toml := renderServerTOML(token, mode, disguises, ports, tier)
	finishWizard("server", toml, "rmtunnel-server")
}

func wizardClient() {
	sectionHeader("Build Kharej Tunnel (Client)")

	fmt.Println(dim("This is the box the real backend (X-UI, a panel, WireGuard, ...) runs on"))
	fmt.Println(dim("or can reach."))
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

	tier := askTierPreset()

	toml := renderClientTOML(token, mode, disguises, tier)
	finishWizard("client", toml, "rmtunnel-client")
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

func askTierPreset() tierPreset {
	fmt.Println(bold("Performance tier (not sure? the default is a safe middle ground):"))
	fmt.Println(menuItem("1", tiers[0].name))
	fmt.Println(menuItem("2", tiers[1].name+" (default)"))
	fmt.Println(menuItem("3", tiers[2].name))
	fmt.Println(menuItem("4", tiers[3].name))
	fmt.Println(dim("Or, for a precise answer, run \"Speed & hardware benchmark\" from the menu after this."))
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
		fmt.Fprintf(&b, "[[ports]]\nlisten = %q\ntarget = %q\n\n", p.Listen, p.Target)
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

// finishWizard shows the generated config, saves it, and offers to install
// it as a systemd service. role is "server" or "client"; unit is the
// matching systemd unit name.
func finishWizard(role, tomlText, unit string) {
	fmt.Println(bold(green("--- config generated ---")))
	fmt.Println(dim(tomlText))

	defaultPath := defaultConfigPath(role)
	path := readLineDefault("save path", defaultPath)

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
		installService(unit, role, path)
	}
	pressEnter()
}

func defaultConfigPath(role string) string {
	if runtime.GOOS == "linux" {
		return "/etc/rmtunnel/" + role + ".toml"
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, role+".toml")
}
