package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// menuHealthCheck runs a battery of system- and per-tunnel checks and prints
// a concrete fix next to anything that fails — the same "don't just say
// something's wrong, say what to run" shape as menuTune's sysctl checks.
// Best-effort on anything sysctl-based: on a non-Linux box (or one without
// systemd) those simply report as not-configured rather than erroring, so
// the per-tunnel checks (which matter everywhere) still run.
func menuHealthCheck(tunnels []tunnelRef) {
	sectionHeader("Health Check")

	fmt.Println(bold(magenta("System")))
	checkFact("running as root", isRoot(), "re-run with sudo — most of this needs it")
	_, err := exec.LookPath("systemctl")
	checkFact("systemd present", err == nil, "this box doesn't use systemd — service install/manage won't work")

	cc := strings.TrimSpace(sysctlGet("net.ipv4.tcp_congestion_control"))
	checkFact(fmt.Sprintf("congestion control = bbr (currently %q)", cc), cc == "bbr", "run \"Tune server\" from the main menu")
	qd := strings.TrimSpace(sysctlGet("net.core.default_qdisc"))
	checkFact(fmt.Sprintf("qdisc = fq (currently %q)", qd), qd == "fq", "run \"Tune server\" from the main menu")

	rmemMax := sysctlInt("net.core.rmem_max")
	wmemMax := sysctlInt("net.core.wmem_max")

	fmt.Println()
	fmt.Println(bold(magenta("Tunnels")))
	if len(tunnels) == 0 {
		fmt.Println(dim("  none configured."))
		pressEnter()
		return
	}

	// usedAddrs catches two tunnels accidentally fighting over the same
	// port — an easy mistake once a box runs more than a couple of them,
	// and one that otherwise only shows up as one tunnel mysteriously
	// failing to bind after the other one starts first.
	usedAddrs := map[string]string{}
	claim := func(addr, what, owner string) {
		key := what + ":" + addr
		if prev, dup := usedAddrs[key]; dup {
			fmt.Printf("    %s %s %s is also used by %s\n", red("✖"), what, addr, prev)
		} else {
			usedAddrs[key] = owner
		}
	}

	for _, t := range tunnels {
		fmt.Println("  " + roleTag(t.Role) + " " + bold(t.Name) + dim(":"))
		cfg, err := LoadConfig(t.Path, t.Role)
		if err != nil {
			fmt.Println("    " + red("✖ config error: ") + err.Error())
			continue
		}

		active, _ := serviceStatus(t.unit())
		checkFact("service active", active, "start it from \"Manage tunnels\"")
		checkFact("token looks strong (>= 16 chars)", len(cfg.Token) >= 16, "regenerate it via Edit — a short token is easier to brute-force")

		if rmemMax > 0 && cfg.RecvBuf > rmemMax {
			fmt.Printf("    %s recv_buf (%d) exceeds net.core.rmem_max (%d) — the OS silently caps it there\n", yellow("⚠"), cfg.RecvBuf, rmemMax)
		}
		if wmemMax > 0 && cfg.SendBuf > wmemMax {
			fmt.Printf("    %s send_buf (%d) exceeds net.core.wmem_max (%d) — the OS silently caps it there\n", yellow("⚠"), cfg.SendBuf, wmemMax)
		}

		owner := t.Role + "/" + t.Name
		if t.Role == "server" {
			for _, p := range cfg.Ports {
				claim(p.Listen, "listen", owner)
			}
		}
		for _, d := range cfg.Disguise {
			if d.Enabled && d.ListenAddr != "" {
				claim(d.ListenAddr, "disguise listen", owner)
			}
		}
	}
	pressEnter()
}

// checkFact prints one pass/fail line, with a concrete fix suggestion on
// failure — never just "this is wrong."
func checkFact(label string, ok bool, fix string) {
	if ok {
		fmt.Println("    " + green("✔") + " " + label)
		return
	}
	fmt.Println("    " + red("✖") + " " + label)
	fmt.Println("      " + dim("fix: "+fix))
}

func sysctlGet(key string) string {
	out, err := run("sysctl", "-n", key)
	if err != nil {
		return ""
	}
	return out
}

func sysctlInt(key string) int {
	v, err := strconv.Atoi(strings.TrimSpace(sysctlGet(key)))
	if err != nil {
		return 0
	}
	return v
}

func isRoot() bool {
	return os.Geteuid() == 0
}
