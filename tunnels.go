package main

// Multiple tunnels can run on one box — an Iran box might forward several
// unrelated services, or a box might run both a server and a client tunnel
// for different purposes. Each one is a named config file plus a systemd
// *template* unit instance (rmtunnel-server@<name> / rmtunnel-client@<name>),
// the standard systemd pattern for "several instances of the same service
// differing by one parameter" — see systemd/*.service.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const tunnelsRoot = "/etc/rmtunnel/tunnels"

var validNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func isValidTunnelName(name string) bool {
	return name != "" && len(name) <= 64 && validNameRe.MatchString(name)
}

func tunnelDir(role string) string {
	return filepath.Join(tunnelsRoot, role)
}

func tunnelConfigPath(role, name string) string {
	return filepath.Join(tunnelDir(role), name+".toml")
}

func systemdUnitInstance(role, name string) string {
	return "rmtunnel-" + role + "@" + name
}

type tunnelRef struct {
	Role string // "server" | "client"
	Name string
	Path string
}

func (t tunnelRef) unit() string { return systemdUnitInstance(t.Role, t.Name) }

// listTunnels scans both role directories for configured tunnels, sorted by
// role then name so the listing is stable across runs.
func listTunnels() []tunnelRef {
	var out []tunnelRef
	for _, role := range []string{"server", "client"} {
		entries, err := os.ReadDir(tunnelDir(role))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".toml")
			out = append(out, tunnelRef{Role: role, Name: name, Path: tunnelConfigPath(role, name)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func installTunnelService(role, name, configPath string) {
	installService(systemdUnitInstance(role, name), role, configPath)
}

// --- menu ---------------------------------------------------------------

func menuManageTunnels() {
	for {
		sectionHeader("Manage Tunnels")
		tunnels := listTunnels()
		if len(tunnels) == 0 {
			fmt.Println(dim("no tunnels configured yet — build one from the main menu first."))
			pressEnter()
			return
		}

		for i, t := range tunnels {
			status := "-"
			if runtime.GOOS == "linux" {
				active, _ := serviceStatus(t.unit())
				status = red("inactive")
				if active {
					status = green("active")
				}
			}
			fmt.Printf("  %s) [%s] %-20s %s\n", bold(cyan(fmt.Sprint(i+1))), t.Role, t.Name, status)
		}
		fmt.Println(menuItem("0", "back"))

		choice := readLine("choice: ")
		if choice == "0" || choice == "" {
			return
		}
		idx := indexFromChoice(choice, len(tunnels))
		if idx < 0 {
			fmt.Println(red("invalid choice."))
			pressEnter()
			continue
		}
		tunnelActionMenu(tunnels[idx])
	}
}

func indexFromChoice(choice string, n int) int {
	i := 0
	if _, err := fmt.Sscanf(choice, "%d", &i); err != nil {
		return -1
	}
	if i < 1 || i > n {
		return -1
	}
	return i - 1
}

func tunnelActionMenu(t tunnelRef) {
	for {
		sectionHeader(fmt.Sprintf("Tunnel: %s (%s)", t.Name, t.Role))
		active, detail := serviceStatus(t.unit())
		statusLabel := red("● inactive")
		if active {
			statusLabel = green("● active")
		}
		fmt.Printf("  status: %s  %s\n\n", statusLabel, dim(detail))

		fmt.Println(menuItem("1", "Edit"))
		fmt.Println(menuItem("2", "Start"))
		fmt.Println(menuItem("3", "Stop"))
		fmt.Println(menuItem("4", "Restart"))
		fmt.Println(menuItem("5", "View logs"))
		fmt.Println(menuItem("6", "Delete"))
		fmt.Println(menuItem("0", "back"))

		switch readLine("choice: ") {
		case "1":
			editTunnel(t)
		case "2":
			run("systemctl", "start", t.unit())
			fmt.Println(green("started."))
			pressEnter()
		case "3":
			run("systemctl", "stop", t.unit())
			fmt.Println(green("stopped."))
			pressEnter()
		case "4":
			run("systemctl", "restart", t.unit())
			fmt.Println(green("restarted."))
			pressEnter()
		case "5":
			out, _ := run("journalctl", "-u", t.unit(), "-n", "60", "--no-pager")
			fmt.Println(out)
			pressEnter()
		case "6":
			if deleteTunnel(t) {
				return
			}
		case "0", "":
			return
		default:
			fmt.Println(red("invalid choice."))
			pressEnter()
		}
	}
}

// editTunnel loads an existing tunnel's config, shows what it currently
// has, and lets you change one thing at a time — token, forwarded ports,
// disguises, or performance tier — each saved and (optionally) applied by
// restarting the service immediately. Which changes need the same edit
// repeated on the peer is called out explicitly, because this tool has no
// way to reach the peer itself — see docs/CENSORSHIP.md on why there is no
// "push to the other side" channel here (the honest reason: it's one more
// thing that could be blocked, not that it wouldn't be convenient).
func editTunnel(t tunnelRef) {
	sectionHeader("Edit: " + t.Name + " (" + t.Role + ")")
	cfg, err := LoadConfig(t.Path, t.Role)
	if err != nil {
		fmt.Println(red("failed to read this tunnel's config: " + err.Error()))
		pressEnter()
		return
	}

	fmt.Println(bold("current settings:"))
	fmt.Printf("  mode: %s\n", cfg.Mode)
	for _, d := range cfg.Disguise {
		state := "disabled"
		if d.Enabled {
			state = "enabled"
		}
		addr := d.ListenAddr
		if t.Role == "client" {
			addr = d.ServerAddr
		}
		fmt.Printf("  disguise %s: %s (%s)\n", d.Type, state, addr)
	}
	if t.Role == "server" {
		for _, p := range cfg.Ports {
			udp := ""
			if p.UDP {
				udp = " +UDP"
			}
			fmt.Printf("  port: %s%s\n", formatPortSpec(p), udp)
		}
	}
	fmt.Println()

	fmt.Println(menuItem("1", "change token "+yellow("(needs updating on the peer too)")))
	nextOpt := 2
	portsOpt := -1
	if t.Role == "server" {
		fmt.Println(menuItem(fmt.Sprint(nextOpt), "change forwarded ports "+dim("(local only)")))
		portsOpt = nextOpt
		nextOpt++
	}
	disguiseOpt := nextOpt
	fmt.Println(menuItem(fmt.Sprint(nextOpt), "change disguises "+yellow("(needs matching change on the peer)")))
	nextOpt++
	tierOpt := nextOpt
	fmt.Println(menuItem(fmt.Sprint(nextOpt), "change performance tier "+dim("(local only)")))
	fmt.Println(menuItem("0", "back (no changes)"))

	choice := readLine("choice: ")
	changed := true
	switch {
	case choice == "1":
		cfg.Token = readLineDefault("new token", cfg.Token)
		fmt.Println(yellow("⚠ update the peer's token too, or it will stop connecting."))

	case portsOpt > 0 && choice == fmt.Sprint(portsOpt):
		fmt.Println(dim("current: " + joinPortSpecs(cfg.Ports)))
		cfg.Ports = askPorts()

	case choice == fmt.Sprint(disguiseOpt):
		cfg.Disguise = disguiseAnswersToConfig(askDisguises(t.Role == "server"), t.Role == "server")
		fmt.Println(yellow("⚠ update the peer's disguise list to match, or some/all failover paths will be lost."))

	case choice == fmt.Sprint(tierOpt):
		applyTierToConfig(cfg, askTierPreset(""))

	default:
		changed = false
	}

	if !changed {
		return
	}

	if err := saveConfig(cfg, t.Path); err != nil {
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

func joinPortSpecs(ports []PortMap) string {
	specs := make([]string, len(ports))
	for i, p := range ports {
		specs[i] = formatPortSpec(p)
		if p.UDP {
			specs[i] += "(udp)"
		}
	}
	return strings.Join(specs, ", ")
}

func disguiseAnswersToConfig(answers []disguiseAnswer, isServer bool) []DisguiseConfig {
	out := make([]DisguiseConfig, len(answers))
	for i, d := range answers {
		dc := DisguiseConfig{
			Type: d.Type, Enabled: true, Domain: d.Domain, Path: d.Path,
			CertFile: d.CertFile, KeyFile: d.KeyFile, Insecure: d.Insecure,
		}
		if isServer {
			dc.ListenAddr = d.Addr
		} else {
			dc.ServerAddr = d.Addr
		}
		out[i] = dc
	}
	return out
}

func applyTierToConfig(cfg *Config, t tierPreset) {
	cfg.MinIdle = t.minIdle
	cfg.MaxIdle = t.maxIdle
	cfg.BufferSize = t.bufferSize
	cfg.RecvBuf = t.recvBuf
	cfg.SendBuf = t.sendBuf
	cfg.MaxStreamsPerSession = t.maxStreamsPerSession
	cfg.MuxFrameSize = t.muxFrameSize
	cfg.MuxRecvBuffer = t.muxRecvBuffer
	cfg.MuxStreamBuffer = t.muxStreamBuffer
}

func saveConfig(cfg *Config, path string) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func deleteTunnel(t tunnelRef) bool {
	if !confirm(red("delete tunnel \""+t.Name+"\" ("+t.Role+")? this stops it and removes its config"), false) {
		return false
	}
	run("systemctl", "disable", "--now", t.unit())
	os.Remove(t.Path)
	fmt.Println(green("deleted."))
	pressEnter()
	return true
}
