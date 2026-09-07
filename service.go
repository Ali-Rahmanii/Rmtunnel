package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

const serviceTemplate = `[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=2
LimitNOFILE=1048576
%sNoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`

// installService writes and enables a systemd unit for role ("server" or
// "client") running against the config at configPath. Linux-only — callers
// check runtime.GOOS first.
func installService(unit, role, configPath string) {
	binPath, err := exec.LookPath("rmtunnel")
	if err != nil {
		// Not on PATH yet — assume the currently running binary is the one
		// to use, and that it lives (or will be copied to) the usual place.
		binPath = "/usr/local/bin/rmtunnel"
		self, err := os.Executable()
		if err == nil && self != binPath {
			if data, err := os.ReadFile(self); err == nil {
				if err := os.WriteFile(binPath, data, 0o755); err != nil {
					fmt.Println(red("failed to copy the binary to " + binPath + ": " + err.Error()))
					fmt.Println(dim("copy it manually: cp " + self + " " + binPath))
				} else {
					fmt.Println(green("binary copied to " + binPath))
				}
			}
		}
	}

	extra := ""
	if role == "server" {
		extra = "AmbientCapabilities=CAP_NET_BIND_SERVICE\n"
	}
	execStart := fmt.Sprintf("%s %s %s", binPath, role, configPath)
	writeAndEnableUnit(unit, "rmtunnel "+role, execStart, extra)
}

// installPaqetService is installService's paqet-shaped counterpart: same
// systemd unit machinery, but paqet's own CLI ("paqet run -c <path>", no
// role argument) and its need for raw sockets (no AmbientCapabilities
// shortcut for that — see paqet_install.go, it just runs as root like its
// own docs say to).
func installPaqetService(unit, configPath string) {
	execStart := fmt.Sprintf("%s run -c %s", paqetBinPath, configPath)
	writeAndEnableUnit(unit, "paqet ("+unit+")", execStart, "")
}

// writeAndEnableUnit is the actual "write a systemd template-instance unit
// and start it" step, shared by every engine this project manages.
func writeAndEnableUnit(unit, description, execStart, extra string) {
	unitText := fmt.Sprintf(serviceTemplate, description, execStart, extra)
	unitPath := "/etc/systemd/system/" + unit + ".service"

	if err := os.WriteFile(unitPath, []byte(unitText), 0o644); err != nil {
		fmt.Println(red("failed to write the service file: " + err.Error()))
		return
	}
	fmt.Println(green("service file written: " + unitPath))

	run("systemctl", "daemon-reload")
	run("systemctl", "enable", "--now", unit)
	fmt.Println(green("service " + unit + " enabled and started."))
	fmt.Println(dim("to view logs:  journalctl -u " + unit + " -f"))
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func serviceStatus(unit string) (active bool, detail string) {
	out, err := run("systemctl", "is-active", unit)
	out = trimNL(out)
	if err != nil {
		return false, out
	}
	return out == "active", out
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func menuUninstall() {
	sectionHeader("Uninstall")
	if runtime.GOOS != "linux" {
		fmt.Println(dim("this section only applies on Linux."))
		pressEnter()
		return
	}
	tunnels := listTunnels()
	if !confirm(red(fmt.Sprintf("are you sure? this stops and removes all %d configured tunnel(s), the binary, and (optionally) the configs", len(tunnels))), false) {
		return
	}
	for _, t := range tunnels {
		run("systemctl", "disable", "--now", t.unit())
		os.Remove("/etc/systemd/system/" + t.unit() + ".service")
	}
	run("systemctl", "daemon-reload")
	os.Remove("/usr/local/bin/rmtunnel")
	if paqetInstalled() {
		os.Remove(paqetBinPath)
	}
	fmt.Println(green("services and binaries removed."))
	fmt.Println(dim("note: any iptables NOTRACK rules a paqet tunnel added are not reverted — see \"iptables -t raw -L\" / \"iptables -t mangle -L\" if you want them gone too."))
	if confirm("remove the configs (/etc/rmtunnel) too?", false) {
		os.RemoveAll("/etc/rmtunnel")
		fmt.Println(green("configs removed too."))
	}
	pressEnter()
}
