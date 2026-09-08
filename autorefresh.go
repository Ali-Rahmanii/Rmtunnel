package main

// Auto Refresh restarts every configured tunnel on a fixed schedule — a
// periodic safety net some deployments want regardless of whether anything
// is actually wrong (a fresh control-channel/pool from a clean process
// occasionally clears up whatever a slow memory/fd creep from a very long
// uptime might otherwise cause). It's a systemd timer, not a loop inside
// the interactive menu process — the menu isn't running most of the time,
// a timer is.

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

const (
	autoRefreshTimerUnit   = "rmtunnel-autorefresh.timer"
	autoRefreshServiceUnit = "rmtunnel-autorefresh.service"
	autoRefreshTimerPath   = "/etc/systemd/system/" + autoRefreshTimerUnit
	autoRefreshServicePath = "/etc/systemd/system/" + autoRefreshServiceUnit
)

// restartAllTunnelsCLI is "rmtunnel restart-all" — the auto-refresh
// service's own ExecStart, and also usable by hand. Deliberately quiet and
// non-interactive (no confirm(), no color) since a systemd service has no
// TTY to show either to; failures still go to the journal via log.
func restartAllTunnelsCLI() {
	for _, t := range listTunnels() {
		if out, err := run("systemctl", "restart", t.unit()); err != nil {
			fmt.Fprintf(os.Stderr, "restart-all: failed to restart %s: %v\n%s\n", t.unit(), err, out)
		} else {
			fmt.Printf("restart-all: restarted %s\n", t.unit())
		}
	}
}

// currentAutoRefreshHours reads the interval back out of the installed
// timer's own OnUnitActiveSec= line rather than keeping a second, separate
// state file that could drift out of sync with what's actually installed —
// 0 if the timer isn't installed at all.
func currentAutoRefreshHours() int {
	data, err := os.ReadFile(autoRefreshTimerPath)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "OnUnitActiveSec="); ok {
			v = strings.TrimSuffix(v, "h")
			if hours, err := strconv.Atoi(v); err == nil {
				return hours
			}
		}
	}
	return 0
}

func menuAutoRefresh() {
	sectionHeader("Auto Refresh Schedule")
	if runtime.GOOS != "linux" {
		fmt.Println(dim("this section needs systemd — only available on Linux."))
		pressEnter()
		return
	}

	current := currentAutoRefreshHours()
	if current > 0 {
		fmt.Printf("Current interval: every %dh\n", current)
	} else {
		fmt.Println("Currently disabled.")
	}
	fmt.Println()

	answer := readLineDefault("Auto refresh interval in hours (0 to disable)", strconv.Itoa(current))
	hours, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || hours < 0 {
		fmt.Println(red("enter a whole number of hours (0 or more)."))
		pressEnter()
		return
	}

	if hours == 0 {
		if current == 0 {
			pressEnter()
			return
		}
		run("systemctl", "disable", "--now", autoRefreshTimerUnit)
		os.Remove(autoRefreshTimerPath)
		os.Remove(autoRefreshServicePath)
		run("systemctl", "daemon-reload")
		fmt.Println(green("auto refresh disabled."))
		pressEnter()
		return
	}

	binPath, err := exec.LookPath("rmtunnel")
	if err != nil {
		binPath = "/usr/local/bin/rmtunnel"
	}

	service := fmt.Sprintf(`[Unit]
Description=rmtunnel — restart every configured tunnel

[Service]
Type=oneshot
ExecStart=%s restart-all
`, binPath)

	timer := fmt.Sprintf(`[Unit]
Description=rmtunnel — periodic tunnel restart, every %dh

[Timer]
OnBootSec=%dh
OnUnitActiveSec=%dh
Persistent=true

[Install]
WantedBy=timers.target
`, hours, hours, hours)

	if err := os.WriteFile(autoRefreshServicePath, []byte(service), 0o644); err != nil {
		fmt.Println(red("failed to write service file: " + err.Error()))
		pressEnter()
		return
	}
	if err := os.WriteFile(autoRefreshTimerPath, []byte(timer), 0o644); err != nil {
		fmt.Println(red("failed to write timer file: " + err.Error()))
		pressEnter()
		return
	}
	run("systemctl", "daemon-reload")
	if out, err := run("systemctl", "enable", "--now", autoRefreshTimerUnit); err != nil {
		fmt.Println(red("failed to enable the timer:"))
		fmt.Println(dim(out))
		pressEnter()
		return
	}
	fmt.Println(green(fmt.Sprintf("auto refresh enabled — every tunnel restarts every %dh.", hours)))
	pressEnter()
}
