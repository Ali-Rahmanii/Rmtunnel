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

// tunnelRoles is every directory listTunnels scans. "paqet-server" and
// "paqet-client" are paqet tunnels (see paqet.go) — a second engine
// entirely, stored as YAML instead of TOML, but living in the same
// directory tree and shown in the same "Manage tunnels" list so a box
// running a mix of engines still has one place to look.
var tunnelRoles = []string{"server", "client", "paqet-server", "paqet-client"}

var validNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func isValidTunnelName(name string) bool {
	return name != "" && len(name) <= 64 && validNameRe.MatchString(name)
}

func tunnelDir(role string) string {
	return filepath.Join(tunnelsRoot, role)
}

// tunnelConfigExt is ".yaml" for a paqet role, ".toml" for rmtunnel's own —
// the one place that distinction is made, so everything else that handles a
// tunnelRef generically (listing, start/stop/restart, logs, delete) never
// has to know or care which engine it is.
func tunnelConfigExt(role string) string {
	if strings.HasPrefix(role, "paqet-") {
		return ".yaml"
	}
	return ".toml"
}

func tunnelConfigPath(role, name string) string {
	return filepath.Join(tunnelDir(role), name+tunnelConfigExt(role))
}

func systemdUnitInstance(role, name string) string {
	return "rmtunnel-" + role + "@" + name
}

type tunnelRef struct {
	Role string // "server" | "client" | "paqet-server" | "paqet-client"
	Name string
	Path string
}

func (t tunnelRef) unit() string  { return systemdUnitInstance(t.Role, t.Name) }
func (t tunnelRef) isPaqet() bool { return strings.HasPrefix(t.Role, "paqet-") }

// listTunnels scans every role directory for configured tunnels, sorted by
// role then name so the listing is stable across runs.
func listTunnels() []tunnelRef {
	var out []tunnelRef
	for _, role := range tunnelRoles {
		ext := tunnelConfigExt(role)
		entries, err := os.ReadDir(tunnelDir(role))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ext)
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

		fmt.Println(dim("   #   role           name                  status"))
		for i, t := range tunnels {
			status := dim("-")
			if runtime.GOOS == "linux" {
				active, _ := serviceStatus(t.unit())
				status = statusBadge(active)
			}
			// Width specs apply to the plain role text before it's colored —
			// padding an already-ANSI-wrapped string counts the escape bytes
			// too and silently wrecks the column alignment this is for.
			roleCol := roleTag(fmt.Sprintf("%-13s", t.Role))
			fmt.Printf("  %s   %s%-21s %s\n", bold(pink(fmt.Sprint(i+1))), roleCol, t.Name, status)
		}
		fmt.Println()
		fmt.Println(menuItem("R", "Restart ALL"))
		fmt.Println(menuItem("H", "Health check"))
		fmt.Println(menuItem("F", "File locations"))
		fmt.Println(menuItem("0", "back"))

		choice := readLine("choice: ")
		switch strings.ToUpper(strings.TrimSpace(choice)) {
		case "0", "":
			return
		case "R":
			restartAllTunnels(tunnels)
			continue
		case "H":
			menuHealthCheck(tunnels)
			continue
		case "F":
			menuFileLocations()
			continue
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

func restartAllTunnels(tunnels []tunnelRef) {
	if !confirm(fmt.Sprintf("restart all %d tunnel(s) now?", len(tunnels)), true) {
		return
	}
	for _, t := range tunnels {
		if _, err := run("systemctl", "restart", t.unit()); err != nil {
			fmt.Println(red("  failed to restart " + t.unit()))
		} else {
			fmt.Println(green("  restarted " + t.unit()))
		}
	}
	pressEnter()
}

func menuFileLocations() {
	sectionHeader("File Locations")
	rows := [][2]string{
		{"binary", "/usr/local/bin/rmtunnel"},
		{"paqet binary", paqetBinPath + " (see paqet.go)"},
		{"tunnel configs", tunnelsRoot + "/<role>/<name>.toml (paqet: .../paqet-<role>/<name>.yaml)"},
		{"systemd units", "/etc/systemd/system/rmtunnel-<role>@<name>.service"},
		{"sysctl tuning", "/etc/sysctl.d/99-rmtunnel.conf"},
		{"example configs", "/etc/rmtunnel/*.toml.example"},
	}
	for _, r := range rows {
		fmt.Printf("  %-16s %s\n", bold(r[0]), dim(r[1]))
	}
	fmt.Println()
	fmt.Println(dim("logs: journalctl -u rmtunnel-<role>@<name> -f"))
	pressEnter()
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
		fmt.Printf("  status: %s  %s\n\n", statusBadge(active), dim(detail))

		fmt.Println(menuItem("1", "Edit"))
		fmt.Println(menuItem("2", "Start"))
		fmt.Println(menuItem("3", "Stop"))
		fmt.Println(menuItem("4", "Restart"))
		fmt.Println(menuItem("5", "View logs"))
		fmt.Println(menuItem("6", "Delete"))
		extraOpt := 7
		switch {
		case t.Role == "paqet-server":
			fmt.Println(menuItem(fmt.Sprint(extraOpt), "Reapply iptables rules "+dim("(NOTRACK — see paqet's README)")))
		case t.Role == "paqet-client":
			fmt.Println(menuItem(fmt.Sprint(extraOpt), "Test connection "+dim("(paqet ping)")))
		default:
			extraOpt = -1
		}
		fmt.Println(menuItem("0", "back"))

		choice := readLine("choice: ")
		switch {
		case choice == "1":
			editTunnel(t)
		case choice == "2":
			run("systemctl", "start", t.unit())
			fmt.Println(green("started."))
			pressEnter()
		case choice == "3":
			run("systemctl", "stop", t.unit())
			fmt.Println(green("stopped."))
			pressEnter()
		case choice == "4":
			run("systemctl", "restart", t.unit())
			fmt.Println(green("restarted."))
			pressEnter()
		case choice == "5":
			out, _ := run("journalctl", "-u", t.unit(), "-n", "60", "--no-pager")
			fmt.Println(out)
			pressEnter()
		case choice == "6":
			if deleteTunnel(t) {
				return
			}
		case extraOpt > 0 && choice == fmt.Sprint(extraOpt) && t.Role == "paqet-server":
			reapplyPaqetIPTables(t)
		case extraOpt > 0 && choice == fmt.Sprint(extraOpt) && t.Role == "paqet-client":
			testPaqetConnection(t)
		case choice == "0" || choice == "":
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
	if t.isPaqet() {
		editPaqetTunnel(t)
		return
	}
	// Wrapped in runWizard because the sub-flows this can enter (change
	// disguises, change performance tier) accept "0" as cancel too — without
	// this, that "0" would panic straight through to the main menu loop
	// instead of just backing out of this edit.
	runWizard(func() { editTunnelBody(t) })
}

func editTunnelBody(t tunnelRef) {
	sectionHeader("Edit: " + t.Name + " (" + t.Role + ")")
	cfg, err := LoadConfig(t.Path, t.Role)
	if err != nil {
		fmt.Println(red("failed to read this tunnel's config: " + err.Error()))
		pressEnter()
		return
	}

	// listens tracks who dials vs. who listens for THIS box under its
	// configured Direction — not raw Role, which under Direction "direct"
	// has the roles of "listens" and "dials" swapped. See Config.dialsOut().
	listens := !cfg.dialsOut()

	fmt.Println(bold(magenta("current settings:")))
	fmt.Printf("  mode: %s (direction: %s)\n", cfg.Mode, cfg.Direction)
	for _, d := range cfg.Disguise {
		state := "disabled"
		if d.Enabled {
			state = "enabled"
		}
		addr := d.ListenAddr
		if !listens {
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
		peerLabel := "Iran server"
		if t.Role == "server" {
			peerLabel = "Kharej box"
		}
		cfg.Disguise = disguiseAnswersToConfig(askDisguises(listens, peerLabel), listens)
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

func disguiseAnswersToConfig(answers []disguiseAnswer, listens bool) []DisguiseConfig {
	out := make([]DisguiseConfig, len(answers))
	for i, d := range answers {
		dc := DisguiseConfig{
			Type: d.Type, Enabled: true, Domain: d.Domain, Path: d.Path,
			CertFile: d.CertFile, KeyFile: d.KeyFile, Insecure: d.Insecure,
			BackupAddrs: d.BackupAddrs,
		}
		if listens {
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
