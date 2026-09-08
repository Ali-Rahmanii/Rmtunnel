package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ensurePaqetBinary offers to download paqet if it isn't already installed —
// the wizard's entry point for both sides, so a fresh box needs nothing
// downloaded by hand first.
func ensurePaqetBinary() bool {
	if paqetInstalled() {
		return true
	}
	if runtime.GOOS != "linux" {
		fmt.Println(dim("automatic paqet download is Linux-only — build/copy it to " + paqetBinPath + " by hand on this OS."))
		return false
	}
	fmt.Println(dim("paqet isn't installed yet."))
	if !confirm("download it now from github.com/hanselime/paqet?", true) {
		return false
	}
	fmt.Println(dim("downloading..."))
	if err := installPaqetBinary(); err != nil {
		fmt.Println(red("failed: " + err.Error()))
		return false
	}
	fmt.Println(green("installed: " + paqetBinPath))
	return true
}

// askPaqetNetwork walks the three things paqet's own README has a user find
// by hand (interface, local IP, gateway MAC) — pre-filled from
// detectNetwork() where that succeeds, always left editable.
func askPaqetNetwork(listenPort string) (iface, ip, mac string) {
	fmt.Println(bold(magenta("Network (for raw packet crafting)")))
	fmt.Println(dim("paqet talks to the network card directly, below the kernel's own TCP stack —"))
	fmt.Println(dim("it needs to know which interface, which local IP, and the gateway's MAC."))
	fmt.Println()

	d := detectNetwork()
	if d.Interface != "" {
		fmt.Println(dim("auto-detected: interface=" + d.Interface + " ip=" + d.LocalIP + " gateway_mac=" + d.RouterMAC))
	} else {
		fmt.Println(dim("couldn't auto-detect — enter these by hand (see paqet's README for how)."))
	}

	iface = readLineDefault("network interface (e.g. eth0, ens3)", d.Interface)
	ip = readLineDefault("this box's local/public IP", d.LocalIP)
	mac = readLineDefault("gateway (router) MAC address", d.RouterMAC)
	return iface, ip + ":" + listenPort, mac
}

// paqetForwardsFromPorts converts rmtunnel's own PortMap list into paqet's
// forward entries. A PortMap with UDP set means "relay UDP too, in
// ADDITION to TCP" (see udp.go) — paqet has no such combined entry, each
// forward is one protocol — so that has to become TWO forward entries
// (tcp and udp) on the same listen/target, not one replacing the other.
// Emitting only udp when UDP was requested (an earlier bug here) silently
// dropped TCP entirely.
func paqetForwardsFromPorts(ports []PortMap) []paqetForward {
	var out []paqetForward
	for _, p := range ports {
		target := p.Target
		// askPorts' "|"-separated multiple-backend syntax (backendpool.go)
		// is health-checked and balanced by rmtunnel's own client — paqet
		// has no equivalent concept in its YAML, so rather than write it
		// invalid config, fall back to the first backend and say so.
		if i := strings.Index(target, "|"); i >= 0 {
			fmt.Println(yellow("⚠ paqet doesn't support multiple backends per port — using only the first: " + target[:i]))
			target = target[:i]
		}
		out = append(out, paqetForward{Listen: p.Listen, Target: target, Protocol: "tcp"})
		if p.UDP {
			out = append(out, paqetForward{Listen: p.Listen, Target: target, Protocol: "udp"})
		}
	}
	return out
}

// askPaqetLogLevel defaults to "info", not paqet's own "none" default —
// "none" was this integration's original default (matching the reference
// config it was built from), but it suppresses every diagnostic paqet would
// otherwise print, which makes a first connection failure impossible to
// debug from the logs. Once a tunnel is confirmed working, Edit can quiet
// it back down.
func askPaqetLogLevel() string {
	return readLineDefault("log level (none, debug, info, warn, error, fatal)", "info")
}

// askPaqetKCP walks the transport.kcp block: a named preset (including the
// "gaming" one matching the aggressive manual config this integration was
// built against), or full manual entry. mustMatchKey is true on the side
// that joins an already-existing tunnel (Iran/client) — it has to enter the
// same key the other side is using, not generate a fresh one; false on the
// initiating side (Kharej/server), which is free to generate one.
func askPaqetKCP(key string, mustMatchKey bool) paqetKCP {
	fmt.Println(bold(magenta("KCP performance preset")))
	for i, p := range paqetKCPPresets {
		fmt.Println(menuItem(fmt.Sprint(i+1), bold(p.name)+" — "+p.blurb))
	}
	fmt.Println(menuItem("0", "cancel"))
	choice := askChoice("choice", "1")
	idx := indexFromChoice(choice, len(paqetKCPPresets))
	if idx < 0 {
		idx = 0
	}

	k := paqetKCP{Block: "aes", Key: key}
	preset := paqetKCPPresets[idx]
	if preset.apply != nil {
		preset.apply(&k)
		fmt.Println()
		return askPaqetBlock(k, mustMatchKey)
	}

	// custom manual
	fmt.Println()
	fmt.Println(dim("manual mode — every value below only applies because mode=manual."))
	k.Mode = "manual"
	k.NoDelay = intp(parseIntDefault(readLineDefault("nodelay (0=off, 1=on — aggressive retransmission)", "1"), 1))
	k.Interval = intp(parseIntDefault(readLineDefault("interval ms (10-5000, lower = more responsive, more CPU)", "10"), 10))
	k.Resend = intp(parseIntDefault(readLineDefault("resend (0=off, 1=aggressive, 2=most aggressive fast-retransmit)", "2"), 2))
	k.NoCongestion = intp(parseIntDefault(readLineDefault("nocongestion (0=TCP-like fairness, 1=disable for max speed)", "1"), 1))
	k.WDelay = boolp(confirm("wdelay (batch writes for throughput, vs. flush immediately for latency)", false))
	k.AckNoDelay = boolp(confirm("acknodelay (ack immediately — lower latency, more bandwidth)", true))
	k.MTU = parseIntDefault(readLineDefault("MTU (50-1500)", "1420"), 1420)
	k.Rcvwnd = parseIntDefault(readLineDefault("receive window (packets)", "2048"), 2048)
	k.Sndwnd = parseIntDefault(readLineDefault("send window (packets)", "2048"), 2048)
	if confirm("enable FEC (forward error correction) for a lossy link?", false) {
		k.Dshard = parseIntDefault(readLineDefault("FEC data shards", "10"), 10)
		k.Pshard = parseIntDefault(readLineDefault("FEC parity shards", "3"), 3)
	}
	k.Smuxbuf = parseIntDefault(readLineDefault("smux receive buffer (bytes)", "4194304"), 4194304)
	k.Streambuf = parseIntDefault(readLineDefault("per-stream buffer (bytes)", "2097152"), 2097152)
	fmt.Println()
	return askPaqetBlock(k, mustMatchKey)
}

func askPaqetBlock(k paqetKCP, mustMatchKey bool) paqetKCP {
	fmt.Println(bold(magenta("Encryption")))
	fmt.Println(dim("aes is the safe default. \"none\"/\"null\" disable authentication — anyone"))
	fmt.Println(dim("with your server's IP and port could connect. See paqet's README."))
	k.Block = readLineDefault("encryption block (aes, aes-128-gcm, xor, none, null, ...)", "aes")

	noAuth := k.Block == "none" || k.Block == "null"
	switch {
	case mustMatchKey && !noAuth:
		// Joining an existing tunnel: a freshly generated key here would
		// simply not match what Kharej is already running, and the two
		// sides would never authenticate — this is exactly the bug an
		// earlier version of this wizard had (it silently generated a new
		// key on both sides instead of asking Iran to enter Kharej's).
		k.Key = ""
		for k.Key == "" {
			k.Key = readLine("encryption key (must match Kharej's exactly): ")
		}
	case !noAuth && k.Key == "":
		k.Key = genToken()
		fmt.Println(green("key generated: ") + bold(k.Key))
		fmt.Println(yellow("⚠ enter this exact key on the Iran side too."))
	}
	return k
}

// wizardPaqetKharej builds the Kharej-side config: paqet role "server" —
// the side that listens and holds the real backend. This has to exist and
// be running before the Iran side can connect (see printOrderGuidance).
func wizardPaqetKharej(name string) {
	sectionHeader("Build Paqet Tunnel — Kharej (paqet server)")
	if !ensurePaqetBinary() {
		pressEnter()
		return
	}
	fmt.Println()

	port := readLineDefault("listen port (NOT 80/443 — iptables rules below would affect this box's own outbound traffic too)", "7979")
	iface, addr, mac := askPaqetNetwork(port)
	fmt.Println()

	kcp := askPaqetKCP("", false)
	fmt.Println()

	conn := parseIntDefault(readLineDefault("number of underlying connections (1-256)", "1"), 1)
	logLevel := askPaqetLogLevel()

	c := &paqetConf{
		Role:   "server",
		Log:    paqetLog{Level: logLevel},
		Listen: &paqetListen{Addr: ":" + port},
		Network: paqetNetwork{
			Interface: iface,
			IPv4:      paqetAddr{Addr: addr, RouterMAC: mac},
			TCP:       paqetTCP{LocalFlag: []string{"PA"}},
		},
		Transport: paqetTransport{Protocol: "kcp", Conn: conn, KCP: kcp},
	}

	finishPaqetWizard("paqet-server", name, c, port)
}

// wizardPaqetIran builds the Iran-side config: paqet role "client" — the
// side that dials out first and exposes forward/socks5 ports to real users.
// Kharej (above) must already be configured and running.
func wizardPaqetIran(name string) {
	sectionHeader("Build Paqet Tunnel — Iran (paqet client)")
	if !ensurePaqetBinary() {
		pressEnter()
		return
	}
	fmt.Println()

	serverAddr := readLine("Kharej's address (ip:port — must match its listen port): ")
	fmt.Println()

	// The client dials out on an OS-assigned port (":0") unless exactly one
	// connection is wanted on a fixed port — conf.go's own validation
	// requires this pairing, so the wizard just enforces it directly rather
	// than letting the user hit a cryptic rejection from paqet itself.
	conn := parseIntDefault(readLineDefault("number of underlying connections (1-256)", "1"), 1)
	listenPort := "0"
	if conn == 1 {
		listenPort = readLineDefault("local port to dial from (0 = OS-assigned, recommended)", "0")
	} else {
		fmt.Println(dim("more than one connection requires an OS-assigned local port (0) — using that."))
	}
	iface, addr, mac := askPaqetNetwork(listenPort)
	fmt.Println()

	// mustMatchKey=true: this side is joining a tunnel Kharej already
	// created — it needs Kharej's exact key, not a freshly generated one.
	kcp := askPaqetKCP("", true)
	fmt.Println()

	ports := askPorts(false)
	forwards := paqetForwardsFromPorts(ports)
	fmt.Println()

	var socks5 []paqetSocks5
	if confirm("also expose a SOCKS5 proxy through this tunnel?", false) {
		s := paqetSocks5{Listen: readLineDefault("SOCKS5 listen address", "127.0.0.1:1080")}
		s.Username = readLineDefault("SOCKS5 username (blank = no auth)", "")
		if s.Username != "" {
			s.Password = readLineDefault("SOCKS5 password", "")
		}
		socks5 = append(socks5, s)
	}
	logLevel := askPaqetLogLevel()

	c := &paqetConf{
		Role:    "client",
		Log:     paqetLog{Level: logLevel},
		SOCKS5:  socks5,
		Forward: forwards,
		Network: paqetNetwork{
			Interface: iface,
			IPv4:      paqetAddr{Addr: addr, RouterMAC: mac},
			TCP:       paqetTCP{LocalFlag: []string{"PA"}, RemoteFlag: []string{"PA"}},
		},
		Server:    &paqetServerAddr{Addr: serverAddr},
		Transport: paqetTransport{Protocol: "kcp", Conn: conn, KCP: kcp},
	}

	finishPaqetWizard("paqet-client", name, c, "")
}

// reapplyPaqetIPTables re-runs (idempotently — see applyPaqetIPTables) the
// NOTRACK/RST-drop rules for a paqet-server tunnel, for when the wizard's
// own attempt at setup time failed or was skipped. Available from "Manage
// tunnels" so fixing this doesn't require rebuilding the tunnel.
func reapplyPaqetIPTables(t tunnelRef) {
	sectionHeader("Reapply iptables rules: " + t.Name)
	c, err := loadPaqetConf(t.Path)
	if err != nil {
		fmt.Println(red("failed to read this tunnel's config: " + err.Error()))
		pressEnter()
		return
	}
	if c.Listen == nil || c.Listen.Addr == "" {
		fmt.Println(red("this config has no listen address to apply rules for."))
		pressEnter()
		return
	}
	port := portOf(c.Listen.Addr)
	fmt.Println(dim("applying NOTRACK/RST-drop rules for port " + port + "..."))
	if err := applyPaqetIPTables(port); err != nil {
		fmt.Println(red("failed: " + err.Error()))
	} else if verifyPaqetIPTables(port) {
		fmt.Println(green("✔ applied and confirmed active."))
		persistPaqetIPTables()
	} else {
		fmt.Println(red("✖ iptables reported success but the rules aren't showing as active —"))
		fmt.Println(red("  this system may use nftables without iptables-legacy; apply them by hand."))
	}
	pressEnter()
}

// testPaqetConnection runs paqet's own connectivity probe (paqet ping) for
// a paqet-client tunnel — the tool its own docs point to first when
// troubleshooting ("Use paqet ping -c config.yaml to test the connection").
func testPaqetConnection(t tunnelRef) {
	sectionHeader("Test connection: " + t.Name)
	if !paqetInstalled() {
		fmt.Println(red("paqet isn't installed at " + paqetBinPath + "."))
		pressEnter()
		return
	}
	fmt.Println(dim(paqetBinPath + " ping -c " + t.Path))
	fmt.Println()
	out, err := run(paqetBinPath, "ping", "-c", t.Path)
	fmt.Println(out)
	if err != nil {
		fmt.Println(red("ping failed: " + err.Error()))
		fmt.Println(dim("also try \"paqet dump -p <port>\" on the Kharej box to see if packets are arriving at all."))
	} else {
		fmt.Println(green("reachable."))
	}
	pressEnter()
}

// printPaqetSettings shows everything about a paqet tunnel worth seeing at
// a glance — not just the couple of fields most likely to be edited.
func printPaqetSettings(c *paqetConf) {
	fmt.Println(bold(magenta("current settings:")))
	fmt.Printf("  role: %s   log level: %s\n", c.Role, c.Log.Level)
	fmt.Printf("  network: interface=%s  ip=%s  gateway_mac=%s\n", c.Network.Interface, c.Network.IPv4.Addr, c.Network.IPv4.RouterMAC)
	fmt.Printf("  transport: protocol=%s  connections=%d\n", c.Transport.Protocol, c.Transport.Conn)
	k := c.Transport.KCP
	fmt.Printf("  kcp: mode=%s  block=%s  mtu=%d  rcvwnd=%d  sndwnd=%d\n", k.Mode, k.Block, k.MTU, k.Rcvwnd, k.Sndwnd)
	fmt.Printf("       smuxbuf=%d  streambuf=%d", k.Smuxbuf, k.Streambuf)
	if k.Dshard > 0 {
		fmt.Printf("  fec=%d/%d", k.Dshard, k.Pshard)
	}
	fmt.Println()
	if c.Role == "client" {
		fmt.Printf("  dials: %s\n", c.Server.Addr)
		for _, f := range c.Forward {
			fmt.Printf("  forward: %s -> %s (%s)\n", f.Listen, f.Target, f.Protocol)
		}
		for _, s := range c.SOCKS5 {
			auth := "no auth"
			if s.Username != "" {
				auth = "user " + s.Username
			}
			fmt.Printf("  socks5: %s (%s)\n", s.Listen, auth)
		}
	} else {
		fmt.Printf("  listen: %s\n", c.Listen.Addr)
	}
}

// editPaqetTunnel is editTunnel's paqet-shaped counterpart — same idea
// (show current settings, change one thing, save, offer a restart), scaled
// to what actually needs changing often: forwarded ports (Iran/client side)
// and the KCP performance preset (either side, but both sides must match).
func editPaqetTunnel(t tunnelRef) {
	// Wrapped because the "change KCP preset" sub-flow accepts "0" as
	// cancel too — without this, that "0" would panic past this function
	// instead of just backing out of the edit. See tunnels.go's editTunnel.
	runWizard(func() { editPaqetTunnelBody(t) })
}

func editPaqetTunnelBody(t tunnelRef) {
	sectionHeader("Edit: " + t.Name + " (" + t.Role + ")")
	c, err := loadPaqetConf(t.Path)
	if err != nil {
		fmt.Println(red("failed to read this tunnel's config: " + err.Error()))
		pressEnter()
		return
	}

	printPaqetSettings(c)
	fmt.Println()

	fmt.Println(menuItem("1", "change encryption key "+yellow("(needs updating on the peer too)")))
	nextOpt := 2
	portsOpt := -1
	if c.Role == "client" {
		fmt.Println(menuItem(fmt.Sprint(nextOpt), "change forwarded ports "+dim("(local only)")))
		portsOpt = nextOpt
		nextOpt++
	}
	kcpOpt := nextOpt
	fmt.Println(menuItem(fmt.Sprint(nextOpt), "change KCP preset "+yellow("(needs matching change on the peer)")))
	nextOpt++
	logOpt := nextOpt
	fmt.Println(menuItem(fmt.Sprint(nextOpt), "change log level "+dim("(local only — \"info\" if you're debugging a connection issue)")))
	fmt.Println(menuItem("0", "back (no changes)"))

	choice := readLine("choice: ")
	changed := true
	switch {
	case choice == "1":
		c.Transport.KCP.Key = readLineDefault("new key", c.Transport.KCP.Key)
		fmt.Println(yellow("⚠ update the peer's key too, or it will stop connecting."))

	case portsOpt > 0 && choice == fmt.Sprint(portsOpt):
		ports := askPorts(false)
		c.Forward = paqetForwardsFromPorts(ports)

	case choice == fmt.Sprint(kcpOpt):
		c.Transport.KCP = askPaqetKCP(c.Transport.KCP.Key, false)
		fmt.Println(yellow("⚠ update the peer's KCP settings to match, or the tunnel will fail to connect."))

	case choice == fmt.Sprint(logOpt):
		c.Log.Level = askPaqetLogLevel()

	default:
		changed = false
	}

	if !changed {
		return
	}

	yamlText, err := renderPaqetYAML(c)
	if err != nil {
		fmt.Println(red("failed to render config: " + err.Error()))
		pressEnter()
		return
	}
	if err := os.WriteFile(t.Path, []byte(yamlText), 0o600); err != nil {
		fmt.Println(red("failed to save: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println(green("saved."))

	if confirm("restart "+t.unit()+" now to apply this?", true) {
		run("systemctl", "restart", t.unit())
		fmt.Println(green("restarted."))
	}
	pressEnter()
}

// finishPaqetWizard renders, saves, and offers to install the service —
// paqet's own counterpart to wizard.go's finishWizard. iptablesPort is
// non-empty only for the listening (server) side, where paqet's README
// requires the NOTRACK rules or the kernel's own RST packets destabilize
// the connection under real load.
func finishPaqetWizard(role, name string, c *paqetConf, iptablesPort string) {
	yamlText, err := renderPaqetYAML(c)
	if err != nil {
		fmt.Println(red("failed to render config: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println(bold(green("--- config generated ---")))
	fmt.Println(dim(yamlText))

	path := tunnelConfigPath(role, name)
	if runtime.GOOS != "linux" {
		wd, _ := os.Getwd()
		path = filepath.Join(wd, role+"-"+name+".yaml")
	}
	path = readLineDefault("save path", path)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Println(red("failed to create directory: " + err.Error()))
		pressEnter()
		return
	}
	if err := os.WriteFile(path, []byte(yamlText), 0o600); err != nil {
		fmt.Println(red("failed to write file: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println(green("saved: " + path))

	if runtime.GOOS != "linux" {
		fmt.Println(dim("systemd install and iptables rules are Linux-only — copy this config to the real server."))
		pressEnter()
		return
	}

	if iptablesPort != "" {
		fmt.Println()
		fmt.Println(yellow("paqet's README requires these iptables rules on the listening side, or"))
		fmt.Println(yellow("the kernel's own RST packets will destabilize the tunnel under load:"))
		fmt.Printf("  iptables -t raw -A PREROUTING -p tcp --dport %s -j NOTRACK\n", iptablesPort)
		fmt.Printf("  iptables -t raw -A OUTPUT -p tcp --sport %s -j NOTRACK\n", iptablesPort)
		fmt.Printf("  iptables -t mangle -A OUTPUT -p tcp --sport %s --tcp-flags RST RST -j DROP\n", iptablesPort)
		if confirm("apply these now?", true) {
			if err := applyPaqetIPTables(iptablesPort); err != nil {
				fmt.Println(red("failed: " + err.Error()))
			} else if verifyPaqetIPTables(iptablesPort) {
				fmt.Println(green("✔ applied and confirmed active."))
				persistPaqetIPTables()
			} else {
				fmt.Println(red("✖ iptables reported success but the rules aren't showing as active —"))
				fmt.Println(red("  check whether this system uses nftables without iptables-legacy, and"))
				fmt.Println(red("  apply them by hand if so. Retry from \"Manage tunnels\" → this tunnel →"))
				fmt.Println(red("  \"Reapply iptables rules\" once fixed."))
			}
		}
	}

	if confirm("install and start this as a systemd service now?", true) {
		installPaqetService(systemdUnitInstance(role, name), path)
	}
	pressEnter()
}
