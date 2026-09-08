package main

import "fmt"

// menuHelp is a separate screen on purpose — a step-by-step walkthrough for
// each tunnel type is long, and stuffing it into the main menu would bury
// the actual actions under a wall of text nobody re-reads after the first
// run. One line in the main menu ("Help"), everything else lives here.
func menuHelp() {
	for {
		sectionHeader("Help")
		fmt.Println(dim("Step-by-step for setting each tunnel type up. Pick a topic:"))
		fmt.Println()
		fmt.Println(menuItem("1", "rmtunnel — Reverse (Kharej dials Iran, the usual setup)"))
		fmt.Println(menuItem("2", "rmtunnel — Direct (Iran dials Kharej)"))
		fmt.Println(menuItem("3", "paqet — raw-packet + KCP tunnel"))
		fmt.Println(menuItem("4", "Managing tunnels after they're built"))
		fmt.Println(menuItem("5", "Picking a performance tier / benchmarking"))
		fmt.Println(menuItem("0", "back"))

		switch readLine("choice: ") {
		case "1":
			helpReverse()
		case "2":
			helpDirect()
		case "3":
			helpPaqet()
		case "4":
			helpManage()
		case "5":
			helpTiers()
		case "0", "":
			return
		default:
			fmt.Println(red("invalid choice."))
			pressEnter()
		}
	}
}

func helpReverse() {
	sectionHeader("Help — rmtunnel Reverse")
	fmt.Println(bold("Order matters: Iran (server) must be up and listening FIRST."))
	fmt.Println()
	fmt.Println("  1. On the " + bold("Iran") + " box: main menu → " + bold("[1] Build Iran tunnel") + ".")
	fmt.Println("     Direction: " + bold("Reverse") + ". Note the token it generates (or set your own).")
	fmt.Println("  2. Say yes to installing it as a systemd service — it starts immediately")
	fmt.Println("     and stays up, listening for the Kharej side to connect.")
	fmt.Println("  3. On the " + bold("Kharej") + " box: main menu → " + bold("[2] Build Kharej tunnel") + ".")
	fmt.Println("     Direction: " + bold("Reverse") + ". Same token, same disguise addresses (the")
	fmt.Println("     Iran box's public IP/ports you just set up).")
	fmt.Println("  4. Install it as a service too. Check " + bold("Manage tunnels") + " on either box —")
	fmt.Println("     status should read \"active\" within a few seconds.")
	fmt.Println()
	fmt.Println(dim("If it stays \"active\" but no traffic gets through: Health Check (under"))
	fmt.Println(dim("Manage tunnels) catches the most common misconfigurations."))
	pressEnter()
}

func helpDirect() {
	sectionHeader("Help — rmtunnel Direct")
	fmt.Println(bold("Order matters: Kharej (client) must be up and listening FIRST —"))
	fmt.Println(bold("the roles that listen vs. dial swap under Direct."))
	fmt.Println()
	fmt.Println("  1. On the " + bold("Kharej") + " box: main menu → " + bold("[2] Build Kharej tunnel") + ".")
	fmt.Println("     Direction: " + bold("Direct") + ". Engine: " + bold("rmtunnel") + ". This box now LISTENS,")
	fmt.Println("     so its disguise entries ask for a listen port, not a peer address.")
	fmt.Println("  2. Install it as a service — it starts listening immediately.")
	fmt.Println("  3. On the " + bold("Iran") + " box: main menu → " + bold("[1] Build Iran tunnel") + ".")
	fmt.Println("     Direction: " + bold("Direct") + ". Engine: " + bold("rmtunnel") + ". This box now DIALS OUT to")
	fmt.Println("     the Kharej address you just set up — same token either way.")
	fmt.Println("  4. Install it as a service. Check " + bold("Manage tunnels") + " on either box.")
	fmt.Println()
	fmt.Println(dim("Use Direct when Iran's inbound port doesn't get through but its"))
	fmt.Println(dim("outbound does — otherwise Reverse (above) is the simpler default."))
	pressEnter()
}

func helpPaqet() {
	sectionHeader("Help — paqet")
	fmt.Println(bold("Order matters: Kharej (paqet \"server\") must be up FIRST."))
	fmt.Println(dim("paqet is a separate project (github.com/hanselime/paqet) this menu"))
	fmt.Println(dim("downloads, configures, and manages for you — not rmtunnel's own code."))
	fmt.Println()
	fmt.Println("  1. On the " + bold("Kharej") + " box: main menu → " + bold("[2] Build Kharej tunnel") + " → Direction:")
	fmt.Println("     " + bold("Direct") + " → Direct-mode engine: " + bold("paqet") + ". First run offers to")
	fmt.Println("     download the paqet binary automatically.")
	fmt.Println("  2. Pick a listen port (NOT 80/443), confirm the auto-detected network")
	fmt.Println("     interface/IP/gateway MAC (or enter them by hand — see paqet's own")
	fmt.Println("     README for how, if auto-detect comes up empty), pick a KCP preset")
	fmt.Println("     (\"gaming\" for lowest ping), and let it apply the iptables rules")
	fmt.Println("     paqet's docs require — skipping these causes random drops under load.")
	fmt.Println("  3. On the " + bold("Iran") + " box: main menu → " + bold("[1] Build Iran tunnel") + " → Direction:")
	fmt.Println("     " + bold("Direct") + " → engine: " + bold("paqet") + ". Enter Kharej's address, the SAME KCP")
	fmt.Println("     preset, and " + bold("the exact key Kharej generated") + " (this side is asked to")
	fmt.Println("     type it in, not given its own — a mismatched key never connects),")
	fmt.Println("     then the ports to forward — same format as rmtunnel's own port")
	fmt.Println("     question (1232, 1232:2323, or 1232=host:2323).")
	fmt.Println("  4. Both sides show up in " + bold("Manage tunnels") + " under \"paqet-server\"/")
	fmt.Println("     \"paqet-client\" — start/stop/restart/logs work the same as any tunnel.")
	fmt.Println("     Two extra actions live there too: " + bold("Reapply iptables rules") + " (Kharej)")
	fmt.Println("     if the wizard's own attempt didn't take, and " + bold("Test connection") + " (Iran,")
	fmt.Println("     runs \"paqet ping\") to check reachability without digging through logs.")
	fmt.Println()
	fmt.Println(dim("Needs root and libpcap on both boxes (Linux: no extra install needed —"))
	fmt.Println(dim("paqet's own release binaries are statically linked). If it won't connect,"))
	fmt.Println(dim("set log level to \"info\" (the wizard's default) on both sides via Edit and"))
	fmt.Println(dim("check the logs before assuming it's a network/firewall problem."))
	pressEnter()
}

func helpManage() {
	sectionHeader("Help — Managing Tunnels")
	fmt.Println("Main menu → " + bold("[3] Manage tunnels") + " lists everything configured on this box.")
	fmt.Println()
	fmt.Println("  " + bold("Edit") + "     — change one thing (token, ports, disguises/KCP, tier) and")
	fmt.Println("             optionally restart right away. Anything needing the same")
	fmt.Println("             change on the peer is flagged when you make it.")
	fmt.Println("  " + bold("Start/Stop/Restart") + " — plain systemctl actions on this one tunnel.")
	fmt.Println("  " + bold("View logs") + " — the last 60 lines from journalctl.")
	fmt.Println("  " + bold("Delete") + "   — stops it and removes its config permanently.")
	fmt.Println()
	fmt.Println("On the tunnel list itself:")
	fmt.Println("  " + bold("Restart ALL") + "     — every configured tunnel, one confirm.")
	fmt.Println("  " + bold("Health check") + "    — root/systemd/BBR/qdisc, token strength, buffer")
	fmt.Println("                   values that exceed the OS socket-buffer ceiling, and")
	fmt.Println("                   port collisions between two tunnels on this box.")
	fmt.Println("  " + bold("File locations") + "  — every config/unit/log path, in one place.")
	pressEnter()
}

func helpTiers() {
	sectionHeader("Help — Performance Tiers & Benchmarking")
	fmt.Println("A tier name (light/medium/heavy/extreme/insane) is a starting guess.")
	fmt.Println("What actually determines real throughput is whether the buffers match")
	fmt.Println("this link's " + bold("bandwidth-delay product") + " (bandwidth × RTT) — a buffer smaller")
	fmt.Println("than that caps a single connection no matter how fast the link is.")
	fmt.Println()
	fmt.Println("During the wizard, or from main menu → " + bold("[5] Speed & hardware benchmark") + ":")
	fmt.Println("  1. Run " + bold("bench server") + " on one box, " + bold("bench client") + " on the other.")
	fmt.Println("  2. It measures real RTT/throughput and prints the exact config block —")
	fmt.Println("     recv_buf/send_buf/mux_stream_buffer already sized correctly, no math")
	fmt.Println("     required.")
	fmt.Println()
	fmt.Println(dim("The wizard can run this same measurement for you inline (option 1 under"))
	fmt.Println(dim("\"Performance sizing\") instead of you copying numbers over by hand."))
	pressEnter()
}
