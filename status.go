package main

import (
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"
)

// menuStatusTunnels is a live-refreshing dashboard of every configured
// tunnel — one screen to see what's running, what isn't, and what each one
// is actually pointed at, instead of stepping into "Manage tunnel" one at a
// time. Redraws every statusRefreshInterval until interrupted; on Windows
// (no systemd) it renders once and returns, same as the rest of this
// project's Linux-only surfaces.
const statusRefreshInterval = 3 * time.Second

func menuStatusTunnels(tunnels []tunnelRef) {
	sectionHeader("Live Tunnel Status")
	if len(tunnels) == 0 {
		fmt.Println(dim("no tunnels configured yet — build one from the main menu first."))
		pressEnter()
		return
	}
	if runtime.GOOS != "linux" {
		fmt.Println(dim("live status needs systemd — only available on Linux."))
		pressEnter()
		return
	}

	stop := make(chan struct{})
	go func() {
		waitForEnter()
		close(stop)
	}()

	ipv4, ipv6 := localPublicAddrs()
	for {
		clearScreen()
		fmt.Println(purple("▸ ") + bold(cyan("Live Tunnel Status")))
		addrLine := "IPv4: " + dimIfEmpty(ipv4)
		if ipv6 != "" {
			addrLine += "    IPv6: " + ipv6
		}
		fmt.Println(dim(addrLine))
		fmt.Println(dim("Updated: " + time.Now().Format("15:04:05") + "   (press Enter to return)"))
		fmt.Println()
		printStatusTable(tunnels)

		select {
		case <-stop:
			return
		case <-time.After(statusRefreshInterval):
		}
	}
}

func dimIfEmpty(s string) string {
	if s == "" {
		return dim("(unknown)")
	}
	return s
}

func printStatusTable(tunnels []tunnelRef) {
	fmt.Printf("%-16s %-8s %-8s %-10s %s\n", "NAME", "ROLE", "TRANSP", "STATE", "PORTS / REMOTE")
	fmt.Println(strings.Repeat("─", 74))
	for _, t := range tunnels {
		active, _ := serviceStatus(t.unit())
		state := dim("stopped")
		if active {
			state = green("running")
		}

		transp := "-"
		detail := "-"
		if t.isPaqet() {
			transp = "paqet"
			detail = paqetStatusDetail(t)
		} else if cfg, err := LoadConfig(t.Path, t.Role); err == nil {
			transp = cfg.Mode
			detail = statusDetail(t, cfg)
		} else {
			detail = red("config error")
		}

		fmt.Printf("%-16s %-8s %-8s %-10s %s\n", t.Name, t.Role, transp, state, detail)
	}
}

// statusDetail is the "PORTS / REMOTE" column: every forwarded port for a
// role that owns them (always the server, regardless of Direction — see
// server.go's Run), or the address this side dials for whichever role
// actively dials out under this tunnel's Direction (see Config.dialsOut).
// A side that only listens for its peer (Direction "direct"'s client, or
// this box under mode="udp" reverse) shows the listen address instead, so
// the column is never just "-" for a tunnel that's actually configured.
func statusDetail(t tunnelRef, cfg *Config) string {
	if t.Role == "server" {
		if len(cfg.Ports) == 0 {
			return dim("no ports")
		}
		parts := make([]string, len(cfg.Ports))
		for i, p := range cfg.Ports {
			parts[i] = formatPortSpec(p)
		}
		return strings.Join(parts, ", ")
	}

	if cfg.dialsOut() && len(cfg.Disguise) > 0 {
		return cfg.Disguise[0].ServerAddr
	}
	for _, d := range cfg.Disguise {
		if d.Enabled && d.ListenAddr != "" {
			return "listening " + d.ListenAddr
		}
	}
	return dim("-")
}

func paqetStatusDetail(t tunnelRef) string {
	return dim("see \"Manage tunnel\" for paqet details")
}

// localPublicAddrs picks the first global-unicast IPv4/IPv6 address bound to
// a real interface — on a VPS (this project's usual target) that already is
// the box's public address, no NAT to see through, so this needs no external
// "what's my IP" service call.
func localPublicAddrs() (ipv4, ipv6 string) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", ""
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			if ipv4 == "" {
				ipv4 = ip4.String()
			}
		} else if ipv6 == "" {
			ipv6 = ipNet.IP.String()
		}
	}
	return ipv4, ipv6
}

// waitForEnter blocks until the next Enter keypress, ignoring the line's
// content — used to let a live-refreshing screen return to the menu on
// demand without needing a signal handler for Ctrl-C.
func waitForEnter() {
	readLine("")
}
