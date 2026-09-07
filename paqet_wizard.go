package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// askPaqetKCP walks the transport.kcp block: a named preset (including the
// "gaming" one matching the aggressive manual config this integration was
// built against), or full manual entry.
func askPaqetKCP(key string) paqetKCP {
	fmt.Println(bold(magenta("KCP performance preset")))
	for i, p := range paqetKCPPresets {
		fmt.Println(menuItem(fmt.Sprint(i+1), bold(p.name)+" — "+p.blurb))
	}
	choice := readLineDefault("choice", "1")
	idx := indexFromChoice(choice, len(paqetKCPPresets))
	if idx < 0 {
		idx = 0
	}

	k := paqetKCP{Block: "aes", Key: key}
	preset := paqetKCPPresets[idx]
	if preset.apply != nil {
		preset.apply(&k)
		fmt.Println()
		return askPaqetBlock(k)
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
	return askPaqetBlock(k)
}

func askPaqetBlock(k paqetKCP) paqetKCP {
	fmt.Println(bold(magenta("Encryption")))
	fmt.Println(dim("aes is the safe default. \"none\"/\"null\" disable authentication — anyone"))
	fmt.Println(dim("with your server's IP and port could connect. See paqet's README."))
	k.Block = readLineDefault("encryption block (aes, aes-128-gcm, xor, none, null, ...)", "aes")
	if k.Block != "none" && k.Block != "null" && k.Key == "" {
		k.Key = genToken()
		fmt.Println(green("key generated: ") + bold(k.Key))
	}
	fmt.Println(yellow("⚠ this exact key must match on both sides."))
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

	key := ""
	kcp := askPaqetKCP(key)
	fmt.Println()

	conn := parseIntDefault(readLineDefault("number of underlying connections (1-256)", "1"), 1)

	c := &paqetConf{
		Role:   "server",
		Log:    paqetLog{Level: "none"},
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

	key := ""
	kcp := askPaqetKCP(key)
	fmt.Println()

	ports := askPorts()
	var forwards []paqetForward
	for _, p := range ports {
		proto := "tcp"
		if p.UDP {
			proto = "udp"
		}
		forwards = append(forwards, paqetForward{Listen: p.Listen, Target: p.Target, Protocol: proto})
	}
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

	c := &paqetConf{
		Role:    "client",
		Log:     paqetLog{Level: "none"},
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

// editPaqetTunnel is editTunnel's paqet-shaped counterpart — same idea
// (show current settings, change one thing, save, offer a restart), scaled
// to what actually needs changing often: forwarded ports (Iran/client side)
// and the KCP performance preset (either side, but both sides must match).
func editPaqetTunnel(t tunnelRef) {
	sectionHeader("Edit: " + t.Name + " (" + t.Role + ")")
	c, err := loadPaqetConf(t.Path)
	if err != nil {
		fmt.Println(red("failed to read this tunnel's config: " + err.Error()))
		pressEnter()
		return
	}

	fmt.Println(bold(magenta("current settings:")))
	fmt.Printf("  role: %s\n", c.Role)
	fmt.Printf("  kcp mode: %s (block: %s)\n", c.Transport.KCP.Mode, c.Transport.KCP.Block)
	if c.Role == "client" {
		for _, f := range c.Forward {
			fmt.Printf("  forward: %s -> %s (%s)\n", f.Listen, f.Target, f.Protocol)
		}
		for _, s := range c.SOCKS5 {
			fmt.Printf("  socks5: %s\n", s.Listen)
		}
	} else {
		fmt.Printf("  listen: %s\n", c.Listen.Addr)
	}
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
	fmt.Println(menuItem("0", "back (no changes)"))

	choice := readLine("choice: ")
	changed := true
	switch {
	case choice == "1":
		c.Transport.KCP.Key = readLineDefault("new key", c.Transport.KCP.Key)
		fmt.Println(yellow("⚠ update the peer's key too, or it will stop connecting."))

	case portsOpt > 0 && choice == fmt.Sprint(portsOpt):
		ports := askPorts()
		c.Forward = nil
		for _, p := range ports {
			proto := "tcp"
			if p.UDP {
				proto = "udp"
			}
			c.Forward = append(c.Forward, paqetForward{Listen: p.Listen, Target: p.Target, Protocol: proto})
		}

	case choice == fmt.Sprint(kcpOpt):
		c.Transport.KCP = askPaqetKCP(c.Transport.KCP.Key)
		fmt.Println(yellow("⚠ update the peer's KCP settings to match, or the tunnel will fail to connect."))

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
			} else {
				fmt.Println(green("applied."))
				persistPaqetIPTables()
			}
		}
	}

	if confirm("install and start this as a systemd service now?", true) {
		installPaqetService(systemdUnitInstance(role, name), path)
	}
	pressEnter()
}
