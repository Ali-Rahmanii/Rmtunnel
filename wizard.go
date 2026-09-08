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
	Type        string
	Addr        string // listen_addr (server) or server_addr (client)
	BackupAddrs []string
	Domain      string
	Path        string
	CertFile    string
	KeyFile     string
	Insecure    bool

	// kcp-only — see kcpdisguise.go.
	KCPPreset       string
	KCPMTU          int
	KCPInterval     int
	KCPResend       int
	KCPNoDelay      int
	KCPNoCongestion int
	KCPSndWnd       int
	KCPRcvWnd       int
	KCPAckNoDelay   bool
	KCPDataShards   int
	KCPParityShards int
}

// askBackupAddrs offers extra addresses for the same disguise, tried in
// order if the primary one stops working (config.go's BackupAddrs) — only
// meaningful on the dialing side, since there's nothing to fail over to on
// the side being dialed.
func askBackupAddrs() []string {
	raw := readLineDefault("    backup addresses if this one stops working, comma-separated (blank = none)", "")
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func wizardServer() {
	runWizard(wizardServerBody)
}

func wizardServerBody() {
	sectionHeader("Build Iran Tunnel (Server)")

	fmt.Println(dim("This wizard builds the server config. The ports you open here"))
	fmt.Println(dim("are what end users actually connect to — that's true either direction."))
	fmt.Println(dim("Enter 0 at any numbered question to cancel and return to the main menu."))
	fmt.Println()

	direction := askDirection()
	fmt.Println()

	// Reverse: server listens (Kharej dials in). Direct: server dials out
	// to Kharej instead — see Config.Direction in config.go.
	listens := direction != "direct"
	printOrderGuidance(listens)
	fmt.Println()

	if direction == "direct" {
		if engine := askDirectEngine(); engine == "paqet" {
			name := askTunnelName("paqet-client")
			wizardPaqetIran(name)
			return
		}
	}

	mode := askTransportMode(direction)
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

	disguises := askDisguises(listens, "Kharej box")
	if mode == "udp" {
		fmt.Println(yellow("⚠ mode \"udp\" reuses the first enabled disguise above's port for its own"))
		fmt.Println(yellow("  raw UDP pool socket — UDP and that disguise's TCP listener share the"))
		fmt.Println(yellow("  same port number without conflict, being different protocols."))
	}
	fmt.Println()

	ports := askPorts(mode == "udp")
	fmt.Println()

	benchHost := ""
	if !listens {
		benchHost = hostOnly(disguises[0].Addr)
	}
	kharejHost := readLineDefault("Kharej box's address (optional, only used to offer a live benchmark next)", benchHost)
	tier := askTierPreset(kharejHost)

	toml := renderServerTOML(direction, token, mode, disguises, ports, tier)
	finishWizard("server", name, toml)
}

func wizardClient() {
	runWizard(wizardClientBody)
}

func wizardClientBody() {
	sectionHeader("Build Kharej Tunnel (Client)")

	fmt.Println(dim("This is the box the real backend (X-UI, a panel, WireGuard, ...) runs on"))
	fmt.Println(dim("or can reach."))
	fmt.Println(dim("Enter 0 at any numbered question to cancel and return to the main menu."))
	fmt.Println()

	direction := askDirection()
	fmt.Println()

	// Reverse: client dials out to Iran (the usual setup). Direct: client
	// listens instead, and Iran dials it — see Config.Direction in config.go.
	listens := direction == "direct"
	printOrderGuidance(listens)
	fmt.Println()

	if direction == "direct" {
		if engine := askDirectEngine(); engine == "paqet" {
			name := askTunnelName("paqet-server")
			wizardPaqetKharej(name)
			return
		}
	}

	mode := askTransportMode(direction)
	fmt.Println()

	name := askTunnelName("client")
	fmt.Println()

	token := readLineDefault("Security token (must match the server exactly)", "")
	for token == "" {
		fmt.Println(red("token can't be empty."))
		token = readLineDefault("Security token", "")
	}
	fmt.Println()

	disguises := askDisguises(listens, "Iran server")
	fmt.Println()

	benchHost := ""
	if !listens {
		benchHost = hostOnly(disguises[0].Addr)
	}
	tier := askTierPreset(benchHost)

	toml := renderClientTOML(direction, token, mode, disguises, tier)
	finishWizard("client", name, toml)
}

// askDirection asks who dials whom for the tunnel's own control channel and
// pool/mux capacity. This is independent of which box exposes ports to
// users — that's always the "Iran"/server side regardless of the answer
// here. See Config.Direction's doc comment in config.go for the full
// picture; direct.go is where the "direct" answer actually changes runtime
// behavior.
func askDirection() string {
	fmt.Println(bold(magenta("Direction")))
	fmt.Println(dim("Which box dials the other one to set the tunnel up — not which box"))
	fmt.Println(dim("end users connect to (that's always Iran, either way)."))
	fmt.Println()
	fmt.Println(menuItem("1", bold("Reverse")+dim(" (default)")+" — Kharej dials Iran. Try this first."))
	fmt.Println(menuItem("2", bold("Direct")+" — Iran dials Kharej instead. Use this if Iran's inbound"))
	fmt.Println("           port doesn't get through but its outbound does.")
	fmt.Println(menuItem("0", "cancel"))
	if askChoice("choice", "1") == "2" {
		return "direct"
	}
	return "reverse"
}

// printOrderGuidance states which side has to be up first: whichever one
// listens under the chosen direction. Getting this backwards is the most
// common way a first Direct-mode (or even Reverse) attempt fails for a
// reason that has nothing to do with the config itself — the dialing side
// just has nothing to connect to yet.
func printOrderGuidance(thisBoxListens bool) {
	if thisBoxListens {
		fmt.Println(yellow("⚠ this box LISTENS under this direction — set it up and start it before the other side."))
	} else {
		fmt.Println(yellow("⚠ this box DIALS OUT under this direction — set up and start the other side FIRST, or this box has nothing to connect to yet."))
	}
}

// askDirectEngine offers paqet as an alternative to rmtunnel's own native
// Direct-mode dialer, only under Direction "direct" — paqet's own protocol
// is inherently shaped that way (see paqet.go): the ports-owning side
// always dials out first.
func askDirectEngine() string {
	fmt.Println(bold(magenta("Direct-mode engine")))
	fmt.Println(menuItem("1", bold("rmtunnel")+dim(" (default)")+" — this project's own TCP/TCP Mux, dialing out"))
	fmt.Println(menuItem("2", bold("paqet")+" — raw TCP packets + KCP, bypasses the kernel's own connection"))
	fmt.Println("           tracking — needs root and libpcap on both boxes. github.com/hanselime/paqet")
	fmt.Println(menuItem("0", "cancel"))
	if askChoice("choice", "1") == "2" {
		return "paqet"
	}
	return "rmtunnel"
}

// askTransportMode is the "which TCP variant" question — both ends must
// agree, since nothing on the wire negotiates it (see Config.Mode's doc
// comment in config.go). A raw UDP or QUIC/KCP carrier isn't implemented
// here yet; forwarding UDP traffic *through* whichever of these two is
// chosen is a separate, already-supported yes/no in askPorts below.
// askTransportMode asks family first (TCP or UDP), then the specific
// variant within it — the same two-level shape BackPack itself uses. It's
// asked before name/token/ports (see wizardServer/wizardClient) since the
// whole rest of the wizard branches on the answer: a udp-mode tunnel skips
// the TCP-only questions and asks about UDP forwarding differently.
// direction decides whether "UDP — raw datagrams" is offered as a real,
// runnable choice (Reverse only — see udpcarrier.go) or stays a preview
// like the other two UDP variants.
func askTransportMode(direction string) string {
	fmt.Println(bold(magenta("Transport family")))
	fmt.Println(dim("Both ends must use the same one — there's no negotiation on the wire."))
	fmt.Println()
	fmt.Println(menuItem("1", bold("TCP")+dim(" (default)")+" — reliable, works everywhere"))
	fmt.Println(menuItem("2", "UDP — raw datagrams, or a preview of KCP+FEC/QUIC"))
	fmt.Println(menuItem("0", "cancel"))
	if askChoice("choice", "1") == "2" {
		if mode := askUDPFamily(direction); mode != "" {
			return mode
		}
	}
	return askTCPVariant()
}

// askTCPVariant is the "which TCP variant" question — both ends must
// agree, since nothing on the wire negotiates it (see Config.Mode's doc
// comment in config.go). Forwarding UDP traffic *through* whichever of
// these two is chosen is a separate, already-supported yes/no in askPorts
// below — not the same thing as a UDP carrier (see askUDPFamily).
func askTCPVariant() string {
	fmt.Println()
	fmt.Println(bold(magenta("TCP variant")))
	fmt.Println(menuItem("1", "TCP — one connection per session, simplest, lowest overhead"))
	fmt.Println(menuItem("2", bold("TCP Mux")+dim(" (default)")+" — many sessions multiplexed over a few connections, better under concurrent load"))
	fmt.Println(menuItem("0", "cancel"))
	if askChoice("choice", "2") == "1" {
		return "tcp"
	}
	return "tcpmux"
}

// askUDPFamily offers BackPack's three UDP carrier variants. Only "raw
// datagrams" (mode "udp" — see udpcarrier.go) is actually implemented, and
// only under Direction "reverse": its pool socket is inherently
// server-listens/client-dials shaped, the same way paqet's own protocol is
// inherently direct-shaped. The other two stay descriptions, not runnable
// code — each is its own large, separate undertaking (see README.md and
// docs/TUNING.md), and paqet already covers "raw-packet, low-latency" for
// real use today via the Direct-mode engine choice. Returns "udp" if the
// real option was picked, "" if the caller should fall back to a TCP
// variant instead.
func askUDPFamily(direction string) string {
	fmt.Println()
	fmt.Println(bold(magenta("UDP variant")))
	rawLabel := bold("UDP") + " — raw datagrams, for UDP-based services"
	if direction != "reverse" {
		rawLabel = "UDP — raw datagrams" + dim(" (preview — Reverse direction only, for now)")
	}
	fmt.Println(menuItem("1", rawLabel))
	fmt.Println(menuItem("2", bold("UDP + KCP + FEC")+" — low-latency gaming tunnel, reliable UDP with always-on error correction"))
	fmt.Println(menuItem("3", "UDP + QUIC"+" — encrypted TLS 1.3 streams over UDP, self-tuning, great under loss"))
	fmt.Println(menuItem("0", "back"))
	choice := askChoice("choice", "1")

	if choice == "1" && direction == "reverse" {
		fmt.Println()
		fmt.Println(dim("Raw UDP: no framing, no retransmission, no encryption of its own — one"))
		fmt.Println(dim("packet in becomes one packet out, end to end. Best for a UDP protocol"))
		fmt.Println(dim("that already tolerates loss (WireGuard, a game) and wants the least"))
		fmt.Println(dim("overhead the tunnel can add. The control channel stays fully protected"))
		fmt.Println(dim("by whichever disguise you pick next."))
		return "udp"
	}

	if choice == "2" {
		fmt.Println()
		fmt.Println(dim("KCP+FEC isn't a separate transport family here — it's one more disguise"))
		fmt.Println(dim("type, alongside plain/noise/wss, so it keeps this project's own failover"))
		fmt.Println(dim("and backup-address support instead of losing them for something exotic."))
		fmt.Println(dim("Enable \"kcp\" at the next question. TCP Mux (recommended below) is the"))
		fmt.Println(dim("natural pairing — many streams multiplexed over a few KCP sessions,"))
		fmt.Println(dim("exactly how paqet and BackPack both build the same idea."))
		pressEnter()
		return "" // falls through to askTCPVariant, tcpmux recommended
	}

	if choice == "3" {
		fmt.Println()
		fmt.Println(dim("QUIC isn't a separate transport family here either — it's one more"))
		fmt.Println(dim("disguise type, alongside plain/noise/wss/kcp, so it keeps this"))
		fmt.Println(dim("project's own failover and backup-address support. It self-tunes its"))
		fmt.Println(dim("own congestion control, so there's no preset/tuning question like kcp's"))
		fmt.Println(dim("— just the same TLS cert/domain question wss already asks. Enable"))
		fmt.Println(dim("\"quic\" at the next question. TCP Mux (recommended below) is the"))
		fmt.Println(dim("natural pairing, same as with kcp."))
		pressEnter()
		return "" // falls through to askTCPVariant, tcpmux recommended
	}

	fmt.Println()
	fmt.Println(yellow("⚠ raw UDP is Reverse-direction only for now — switch direction to use it."))
	pressEnter()
	return ""
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
// enabled and, for wss, its extra fields. listens decides whether it asks
// for a listen_addr (this box) or a server_addr (peerLabel, the box being
// dialed) — which no longer tracks Role directly once Direction "direct" is
// in play, see wizardServer/wizardClient.
func askDisguises(listens bool, peerLabel string) []disguiseAnswer {
	fmt.Println(bold(magenta("Which anti-filtering methods should be enabled?")))
	fmt.Println(dim("Recommended: enable all three — the dialing side switches between them on its own."))
	fmt.Println(dim("Full explanation of each: docs/CENSORSHIP.md"))
	fmt.Println()

	var out []disguiseAnswer

	if confirm("  wss (TLS+WebSocket, looks like an ordinary HTTPS site — strongest)", true) {
		d := disguiseAnswer{Type: "wss", Path: "/ws"}
		if listens {
			port := readLineDefault("    wss listen port", "443")
			d.Addr = "0.0.0.0:" + port
			d.Domain = readLineDefault("    domain (enter it if you have a real cert, else leave blank)", "")
			d.CertFile = readLineDefault("    cert file path (blank = self-signed)", "")
			if d.CertFile != "" {
				d.KeyFile = readLineDefault("    key file path", "")
			}
		} else {
			ip := readLine("    " + peerLabel + "'s public address: ")
			port := readLineDefault("    its wss port", "443")
			d.Addr = ip + ":" + port
			d.Domain = readLineDefault("    domain (exactly what you set on the listening side, or blank)", "")
			d.Insecure = confirm("    does the listening side use a self-signed cert?", true)
			d.BackupAddrs = askBackupAddrs()
		}
		out = append(out, d)
	}

	if confirm("  noise (encrypted, no fixed protocol signature)", true) {
		d := disguiseAnswer{Type: "noise"}
		if listens {
			port := readLineDefault("    noise listen port", "9001")
			d.Addr = "0.0.0.0:" + port
		} else {
			ip := readLine("    " + peerLabel + "'s public address: ")
			port := readLineDefault("    its noise port", "9001")
			d.Addr = ip + ":" + port
			d.BackupAddrs = askBackupAddrs()
		}
		out = append(out, d)
	}

	if confirm("  plain (raw, fastest but easiest to fingerprint)", true) {
		d := disguiseAnswer{Type: "plain"}
		if listens {
			port := readLineDefault("    plain listen port", "9000")
			d.Addr = "0.0.0.0:" + port
		} else {
			ip := readLine("    " + peerLabel + "'s public address: ")
			port := readLineDefault("    its plain port", "9000")
			d.Addr = ip + ":" + port
			d.BackupAddrs = askBackupAddrs()
		}
		out = append(out, d)
	}

	if confirm("  kcp"+dim(" (low-latency \"gaming\" carrier — reliable UDP, tuned for steady ping)"), false) {
		d := disguiseAnswer{Type: "kcp"}
		if listens {
			port := readLineDefault("    kcp listen port", "9002")
			d.Addr = "0.0.0.0:" + port
		} else {
			ip := readLine("    " + peerLabel + "'s public address: ")
			port := readLineDefault("    its kcp port", "9002")
			d.Addr = ip + ":" + port
			d.BackupAddrs = askBackupAddrs()
		}
		askKCPSettings(&d)
		out = append(out, d)
	}

	if confirm("  quic"+dim(" (TLS 1.3 over UDP, self-tuning — great under loss, looks like HTTP/3)"), false) {
		d := disguiseAnswer{Type: "quic"}
		if listens {
			port := readLineDefault("    quic listen port", "9003")
			d.Addr = "0.0.0.0:" + port
			d.Domain = readLineDefault("    domain (enter it if you have a real cert, else leave blank)", "")
			d.CertFile = readLineDefault("    cert file path (blank = self-signed)", "")
			if d.CertFile != "" {
				d.KeyFile = readLineDefault("    key file path", "")
			}
		} else {
			ip := readLine("    " + peerLabel + "'s public address: ")
			port := readLineDefault("    its quic port", "9003")
			d.Addr = ip + ":" + port
			d.Domain = readLineDefault("    domain (exactly what you set on the listening side, or blank)", "")
			d.Insecure = confirm("    does the listening side use a self-signed cert?", true)
			d.BackupAddrs = askBackupAddrs()
		}
		out = append(out, d)
	}

	for len(out) == 0 {
		fmt.Println(red("you need to enable at least one."))
		out = askDisguises(listens, peerLabel)
	}
	return out
}

// askKCPSettings fills d's KCP fields — a named preset (both ends must
// agree on all of it, none of it is negotiated on the wire) or full manual
// entry. See kcpdisguise.go for what each preset actually means.
func askKCPSettings(d *disguiseAnswer) {
	fmt.Println()
	fmt.Println(bold(magenta("KCP performance preset")))
	for i, p := range kcpPresets {
		fmt.Println(menuItem(fmt.Sprint(i+1), bold(p.name)+" — "+p.blurb))
	}
	choice := readLineDefault("choice", "2") // turbo
	idx := indexFromChoice(choice, len(kcpPresets))
	if idx < 0 {
		idx = 1
	}
	preset := kcpPresets[idx].name
	d.KCPPreset = preset

	if preset != "custom" {
		// Resolve the preset into concrete fields right now, the same way
		// tierPreset's TCP tuning is resolved at wizard time rather than
		// re-derived at load — the config file ends up with both the label
		// and the numbers it means, inspectable without cross-referencing
		// kcpdisguise.go's table.
		tmp := &DisguiseConfig{KCPPreset: preset}
		applyKCPPreset(tmp)
		d.KCPMTU, d.KCPInterval, d.KCPResend = tmp.KCPMTU, tmp.KCPInterval, tmp.KCPResend
		d.KCPNoDelay, d.KCPNoCongestion, d.KCPAckNoDelay = tmp.KCPNoDelay, tmp.KCPNoCongestion, tmp.KCPAckNoDelay
		d.KCPSndWnd, d.KCPRcvWnd = tmp.KCPSndWnd, tmp.KCPRcvWnd
		d.KCPDataShards, d.KCPParityShards = tmp.KCPDataShards, tmp.KCPParityShards
		return
	}

	fmt.Println()
	fmt.Println(dim("custom — every value below applies as entered."))
	d.KCPMTU = parseIntDefault(readLineDefault("MTU (50-1500, keep below the path MTU)", "1250"), 1250)
	d.KCPInterval = parseIntDefault(readLineDefault("interval ms (10-5000, lower = more responsive, more CPU)", "10"), 10)
	d.KCPResend = parseIntDefault(readLineDefault("resend (0=off, 1=aggressive, 2=most aggressive fast-retransmit)", "2"), 2)
	d.KCPNoDelay = parseIntDefault(readLineDefault("nodelay (0=off, 1=on — skip the delayed-ACK wait)", "1"), 1)
	d.KCPNoCongestion = parseIntDefault(readLineDefault("nocongestion (0=TCP-like fairness, 1=disable for max speed)", "1"), 1)
	d.KCPAckNoDelay = confirm("acknodelay (ack immediately — lower latency, more bandwidth)", true)
	d.KCPSndWnd = parseIntDefault(readLineDefault("send window (packets)", "1024"), 1024)
	d.KCPRcvWnd = parseIntDefault(readLineDefault("receive window (packets)", "1024"), 1024)
	if confirm("enable FEC (forward error correction) for a lossy link?", true) {
		d.KCPDataShards = parseIntDefault(readLineDefault("FEC data shards", "10"), 10)
		d.KCPParityShards = parseIntDefault(readLineDefault("FEC parity shards", "3"), 3)
	}
}

// askTierPreset sizes the buffers for this tunnel. A tier name alone is a
// guess; what actually determines whether a single flow gets the link's real
// throughput is whether recv_buf/send_buf and mux_stream_buffer are at least
// the link's bandwidth-delay product (bandwidth × RTT) — see bench.go's
// tunedTier.resolved(). suggestedHost, if non-empty, prefills the live-bench
// address (the client wizard already knows the server's address; the server
// wizard doesn't know the Kharej box's, so it's asked for there instead).
func askTierPreset(suggestedHost string) tierPreset {
	fmt.Println(bold(magenta("Performance sizing")))
	fmt.Println(dim("The single biggest factor is the link's bandwidth-delay product — a buffer"))
	fmt.Println(dim("smaller than bandwidth×RTT caps a single connection's speed no matter how"))
	fmt.Println(dim("fast the link actually is. Measuring beats guessing."))
	fmt.Println()
	fmt.Println(menuItem("1", "run a live benchmark against the other box now (most accurate)"))
	fmt.Println(menuItem("2", "I know the link's bandwidth and RTT — enter them"))
	fmt.Println(menuItem("3", "just pick a tier (light/medium/heavy/extreme/insane)"))
	fmt.Println(menuItem("0", "cancel"))

	switch askChoice("choice", "3") {
	case "1":
		return askLiveBenchTier(suggestedHost)
	case "2":
		return askManualBenchTier()
	default:
		return askStaticTier()
	}
}

// defaultTierIndex is "medium" — the safe pick for an unknown box, and what
// an empty answer to the picker below falls back to.
const defaultTierIndex = 1

func askStaticTier() tierPreset {
	fmt.Println()
	for i, t := range tiers {
		label := bold(t.name)
		if i == defaultTierIndex {
			label += dim(" (default)")
		}
		fmt.Println(menuItem(fmt.Sprint(i+1), label+" — "+t.blurb))
	}
	fmt.Println(menuItem("0", "cancel"))
	choice := askChoice("choice", fmt.Sprint(defaultTierIndex+1))
	if idx := indexFromChoice(choice, len(tiers)); idx >= 0 {
		return tiers[idx]
	}
	return tiers[defaultTierIndex]
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

// quotedList renders ["a", "b"]'s inside — the part between the brackets —
// for a TOML string array.
func quotedList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, ", ")
}

func renderDisguiseBlock(d disguiseAnswer, listens bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[[disguise]]\n")
	fmt.Fprintf(&b, "type = %q\n", d.Type)
	fmt.Fprintf(&b, "enabled = true\n")
	if listens {
		fmt.Fprintf(&b, "listen_addr = %q\n", d.Addr)
	} else {
		fmt.Fprintf(&b, "server_addr = %q\n", d.Addr)
		if len(d.BackupAddrs) > 0 {
			fmt.Fprintf(&b, "backup_addrs = [%s]\n", quotedList(d.BackupAddrs))
		}
	}
	if d.Type == "wss" || d.Type == "quic" {
		fmt.Fprintf(&b, "domain = %q\n", d.Domain)
		if d.Type == "wss" {
			fmt.Fprintf(&b, "path = %q\n", "/ws")
		}
		if listens {
			fmt.Fprintf(&b, "cert_file = %q\n", d.CertFile)
			fmt.Fprintf(&b, "key_file = %q\n", d.KeyFile)
		} else {
			fmt.Fprintf(&b, "insecure_skip_verify = %v\n", d.Insecure)
		}
	}
	if d.Type == "kcp" {
		fmt.Fprintf(&b, "kcp_preset = %q\n", d.KCPPreset)
		fmt.Fprintf(&b, "kcp_mtu = %d\n", d.KCPMTU)
		fmt.Fprintf(&b, "kcp_interval = %d\n", d.KCPInterval)
		fmt.Fprintf(&b, "kcp_resend = %d\n", d.KCPResend)
		fmt.Fprintf(&b, "kcp_nodelay = %d\n", d.KCPNoDelay)
		fmt.Fprintf(&b, "kcp_nocongestion = %d\n", d.KCPNoCongestion)
		fmt.Fprintf(&b, "kcp_acknodelay = %v\n", d.KCPAckNoDelay)
		fmt.Fprintf(&b, "kcp_sndwnd = %d\n", d.KCPSndWnd)
		fmt.Fprintf(&b, "kcp_rcvwnd = %d\n", d.KCPRcvWnd)
		fmt.Fprintf(&b, "kcp_datashards = %d\n", d.KCPDataShards)
		fmt.Fprintf(&b, "kcp_parityshards = %d\n", d.KCPParityShards)
	}
	return b.String()
}

// renderServerTOML and renderClientTOML write every scalar (top-level)
// field — mode, token, and the whole tuning block — BEFORE any [[disguise]]
// or [[ports]] section. This isn't cosmetic: TOML scopes a bare key to the
// most recently opened table, so a scalar written after one of those blocks
// would silently become a field of that block's last entry instead of a
// top-level setting, and vanish — LoadConfig's Undecoded() check now catches
// that on the way back in, but the fix is to never write it that way. See
// the matching comment on Config's own field order in config.go.

func renderServerTOML(direction, token, mode string, disguises []disguiseAnswer, ports []PortMap, tier tierPreset) string {
	listens := direction != "direct"
	var b strings.Builder
	fmt.Fprintf(&b, "# generated by the rmtunnel wizard — %s\n\n", RepoURL)
	fmt.Fprintf(&b, "mode = %q\ndirection = %q\ntoken = %q\n\n", mode, direction, token)
	b.WriteString(renderTuningBlock(tier))
	b.WriteString("\n")
	for _, d := range disguises {
		b.WriteString(renderDisguiseBlock(d, listens))
		b.WriteString("\n")
	}
	for _, p := range ports {
		fmt.Fprintf(&b, "[[ports]]\nlisten = %q\ntarget = %q\nudp = %v\n\n", p.Listen, p.Target, p.UDP)
	}
	return b.String()
}

func renderClientTOML(direction, token, mode string, disguises []disguiseAnswer, tier tierPreset) string {
	listens := direction == "direct"
	var b strings.Builder
	fmt.Fprintf(&b, "# generated by the rmtunnel wizard — %s\n\n", RepoURL)
	fmt.Fprintf(&b, "mode = %q\ndirection = %q\ntoken = %q\n\n", mode, direction, token)
	fmt.Fprintf(&b, "retry_min = \"1s\"\nretry_max = \"30s\"\n")
	b.WriteString(renderTuningBlock(tier))
	b.WriteString("\n")
	for _, d := range disguises {
		b.WriteString(renderDisguiseBlock(d, listens))
		b.WriteString("\n")
	}
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
