package main

import (
	"context"
	"fmt"
	"math"
	"time"
)

// Link Test measures the quality of the path to a tunnel's peer — latency,
// jitter, packet loss — by dialing the real, already-configured disguise
// itself (see dialDisguise in disguise.go) rather than a synthetic ping, so
// the result reflects exactly what the real tunnel traffic experiences on
// that path. Only a tunnel whose Config.dialsOut() is true has a peer
// address to probe — the side that only listens has nothing to dial (see
// menuLinkTest's filtering below).
const (
	linkTestProbes  = 12
	linkTestTimeout = 5 * time.Second
	linkTestGap     = 200 * time.Millisecond
)

type linkProbeResult struct {
	sent, answered int
	rtts           []time.Duration
}

func (r linkProbeResult) stats() (avg, best, worst, jitter time.Duration, lossPct float64) {
	if len(r.rtts) == 0 {
		return 0, 0, 0, 0, 100
	}
	best, worst = r.rtts[0], r.rtts[0]
	var sum time.Duration
	for _, rtt := range r.rtts {
		sum += rtt
		if rtt < best {
			best = rtt
		}
		if rtt > worst {
			worst = rtt
		}
	}
	avg = sum / time.Duration(len(r.rtts))
	var varSum float64
	for _, rtt := range r.rtts {
		diff := float64(rtt - avg)
		varSum += diff * diff
	}
	jitter = time.Duration(math.Sqrt(varSum / float64(len(r.rtts))))
	lossPct = 100 * float64(r.sent-r.answered) / float64(r.sent)
	return
}

func fmtMs(d time.Duration) string {
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// menuLinkTest picks a dialing-side tunnel, measures its link, and offers
// two kinds of follow-up: liveness-timer tuning (suggestTimers — always
// safe, a purely local setting) and a transport recommendation
// (recommendTransport — anything beyond the one safe tcp<->tcpmux flip
// needs a matching change on the peer, which this always states plainly
// rather than silently leaving the two sides mismatched).
func menuLinkTest(tunnels []tunnelRef) {
	sectionHeader("Link Test")
	fmt.Println(dim("Measures latency, jitter and packet loss to the other server, then"))
	fmt.Println(dim("recommends the transport that suits what it finds."))
	fmt.Println()

	var candidates []tunnelRef
	var cfgs []*Config
	for _, t := range tunnels {
		if t.isPaqet() {
			continue // paqet has its own "Test connection" action — see paqet.go
		}
		cfg, err := LoadConfig(t.Path, t.Role)
		if err != nil || !cfg.dialsOut() || len(cfg.Disguise) == 0 {
			continue // nothing this box actively dials for this tunnel
		}
		candidates = append(candidates, t)
		cfgs = append(cfgs, cfg)
	}

	if len(candidates) == 0 {
		fmt.Println(dim("no tunnel here dials out to a peer — nothing to test."))
		fmt.Println(dim("(the side that only listens has no address of its own to probe.)"))
		pressEnter()
		return
	}

	fmt.Println(bold("Which tunnel's link should be tested?"))
	fmt.Println()
	for i, t := range candidates {
		d := cfgs[i].Disguise[0]
		fmt.Println(menuItem(fmt.Sprint(i+1), fmt.Sprintf("%s  %s — %s", t.Name, d.ServerAddr, transportLabel(cfgs[i].Mode, d.Type))))
	}
	fmt.Println(menuItem("0", "back"))
	choice := askChoice("choice", "1")
	idx := indexFromChoice(choice, len(candidates))
	if idx < 0 {
		fmt.Println(red("invalid choice."))
		pressEnter()
		return
	}

	t, cfg := candidates[idx], cfgs[idx]
	d := &cfg.Disguise[0]

	fmt.Println()
	fmt.Printf("Testing the link to %s — this takes about %d seconds...\n", d.ServerAddr, int((linkTestProbes*(linkTestGap+50*time.Millisecond))/time.Second))
	fmt.Println()

	res := runLinkProbes(cfg, d)
	printLinkResults(d.ServerAddr, res)

	if res.answered == 0 {
		fmt.Println(red("Every probe failed — the link may be down, or blocked, right now."))
		fmt.Println(dim("A recommendation needs at least one successful probe; try again once it recovers."))
		pressEnter()
		return
	}

	suggestTimers(t, cfg, res)
	recommendTransport(t, cfg, d, res)
	pressEnter()
}

// runLinkProbes dials the real disguise linkTestProbes times, timing each
// successful dial as one round trip, then immediately closes it — this
// never completes this project's own protocol.go handshake, so it costs the
// server side nothing more than the same "one failed Accept() cycle" a
// stray scanner probe already does (see disguise.go's plainAccepter).
func runLinkProbes(cfg *Config, d *DisguiseConfig) linkProbeResult {
	res := linkProbeResult{sent: linkTestProbes}
	for i := 0; i < linkTestProbes; i++ {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), linkTestTimeout)
		conn, err := dialDisguise(ctx, cfg, d)
		cancel()
		if err == nil {
			res.rtts = append(res.rtts, time.Since(start))
			conn.Close()
			res.answered++
		}
		if i < linkTestProbes-1 {
			time.Sleep(linkTestGap)
		}
	}
	return res
}

func printLinkResults(addr string, res linkProbeResult) {
	avg, best, worst, jitter, loss := res.stats()
	fmt.Println(bold("Results"))
	fmt.Println()
	fmt.Printf("  %-14s: %s\n", "Target", addr)
	fmt.Printf("  %-14s: %d sent, %d answered\n", "Probes", res.sent, res.answered)
	if res.answered > 0 {
		fmt.Printf("  %-14s: %s average  (best %s, worst %s)\n", "Latency", fmtMs(avg), fmtMs(best), fmtMs(worst))
		fmt.Printf("  %-14s: ±%s\n", "Jitter", fmtMs(jitter))
	}
	lossColor := green
	if loss > 20 {
		lossColor = red
	} else if loss > 0 {
		lossColor = yellow
	}
	fmt.Printf("  %-14s: %s\n", "Packet loss", lossColor(fmt.Sprintf("%.0f%%", loss)))
	fmt.Println()
	fmt.Println(dim("This measures the quality of the link, not its raw speed. For"))
	fmt.Println(dim("throughput numbers, use the wizard's live benchmark (rmtunnel bench)."))
	fmt.Println()
}

// roundUpSeconds rounds d up to the nearest whole second — liveness timers
// are configured in whole seconds throughout this project (see
// renderTuningBlock), so a suggestion like "17.482s" would just look wrong
// next to them.
func roundUpSeconds(d time.Duration) time.Duration {
	return (d + time.Second - 1).Truncate(time.Second)
}

// suggestTimers proposes looser liveness timers when the measured RTT+
// jitter is a meaningful fraction of the current ones — a link this slow
// risks a slow-but-alive peer being wrongly declared dead. Both KeepAlive
// (this project's per-socket TCP keepalive, "shared" tuning applied to
// whichever side owns the connection) and Heartbeat (server-only — see
// config.go) are read from the wire by nobody: the peer doesn't need to
// match either one, so applying them here is always safe on its own.
func suggestTimers(t tunnelRef, cfg *Config, res linkProbeResult) {
	_, _, worst, jitter, _ := res.stats()

	suggestedKeepAlive := cfg.KeepAlive.Duration
	if margin := worst + jitter; margin > cfg.KeepAlive.Duration/2 {
		suggestedKeepAlive = roundUpSeconds(margin * 3)
		if suggestedKeepAlive < 15*time.Second {
			suggestedKeepAlive = 15 * time.Second
		}
	}

	showHeartbeat := t.Role == "server"
	suggestedHeartbeat := cfg.Heartbeat.Duration
	if showHeartbeat {
		if margin := worst + jitter; margin > cfg.Heartbeat.Duration {
			suggestedHeartbeat = roundUpSeconds(margin * 2)
			if suggestedHeartbeat < 5*time.Second {
				suggestedHeartbeat = 5 * time.Second
			}
		}
	}

	if suggestedKeepAlive == cfg.KeepAlive.Duration && (!showHeartbeat || suggestedHeartbeat == cfg.Heartbeat.Duration) {
		return
	}

	fmt.Println(bold("Liveness timers"))
	fmt.Println()
	now := "keepalive " + cfg.KeepAlive.Duration.String()
	suggested := "keepalive " + suggestedKeepAlive.String()
	if showHeartbeat {
		now += ", heartbeat " + cfg.Heartbeat.Duration.String()
		suggested += ", heartbeat " + suggestedHeartbeat.String()
	}
	fmt.Printf("  %-10s: %s\n", "Now", now)
	fmt.Printf("  %-10s: %s\n", "Suggested", suggested)
	fmt.Println(dim(fmt.Sprintf("  worst round trip seen was %s with ±%s jitter", fmtMs(worst), fmtMs(jitter))))
	fmt.Println()
	fmt.Println(dim("Looser timers stop a slow-but-alive peer from being declared dead."))
	fmt.Println(dim("This is a local, per-side setting — the peer does not need to match it."))
	fmt.Println()

	if confirm("Apply these timers to \""+t.Name+"\" now?", false) {
		cfg.KeepAlive = Duration{suggestedKeepAlive}
		if showHeartbeat {
			cfg.Heartbeat = Duration{suggestedHeartbeat}
		}
		if err := saveConfig(cfg, t.Path); err != nil {
			fmt.Println(red("failed to save: " + err.Error()))
		} else {
			fmt.Println(green("saved."))
			if confirm("restart "+t.unit()+" now to apply this?", true) {
				run("systemctl", "restart", t.unit())
				fmt.Println(green("restarted."))
			}
		}
	}
	fmt.Println()
}

// transportLabel renders mode+disguise-type as one short human label, the
// same shape used throughout this file's own output.
func transportLabel(mode, disguiseType string) string {
	switch mode {
	case "tcp":
		return "TCP (" + disguiseType + ")"
	case "tcpmux":
		return "TCP Mux (" + disguiseType + ")"
	case "udp":
		return "UDP raw"
	default:
		return mode + " (" + disguiseType + ")"
	}
}

// recommendTransport suggests a Mode switch between "tcp" and "tcpmux" — the
// one transport axis this project can safely flip on its own, since both
// share every disguise type unchanged — when the measured link clearly
// favors one over the other, and separately flags when heavier loss/jitter
// suggests a differently-shaped disguise (kcp+FEC) instead. That second case
// is a disguise-type change, a bigger edit than this menu makes for you, so
// it's pointed at "Manage tunnel → change disguises" (or the wizard's UDP +
// KCP + FEC option) rather than applied here. Mode MUST match on both ends
// with no negotiation on the wire (see Config.Mode's doc comment) — so any
// switch this menu DOES apply always prints exactly what the peer also
// needs changed, right after making it, never leaving that unsaid.
func recommendTransport(t tunnelRef, cfg *Config, d *DisguiseConfig, res linkProbeResult) {
	_, _, _, jitter, loss := res.stats()
	udpBased := d.Type == "kcp" || d.Type == "quic" || cfg.Mode == "udp"

	fmt.Println(bold("Recommendation"))
	fmt.Println()

	switch {
	case loss > 5 || jitter > 60*time.Millisecond:
		if d.Type == "kcp" {
			fmt.Println("  " + bold("keep UDP + KCP + FEC") + " — it's already the right tool for this link")
			fmt.Printf("  • %.0f%% packet loss and ±%s jitter is exactly what always-on FEC is for\n", loss, fmtMs(jitter))
		} else {
			fmt.Println("  " + bold("UDP + KCP + FEC"))
			fmt.Printf("  • %.0f%% packet loss and ±%s jitter — this link is lossy enough that\n", loss, fmtMs(jitter))
			fmt.Println("    always-on forward error correction earns back real, usable latency")
			fmt.Println("  ! this is a disguise-type change, not a same-family Mode flip — set it")
			fmt.Println("    up via \"Manage tunnel\" → change disguises (or the wizard's UDP + KCP")
			fmt.Println("    + FEC option) on BOTH ends; this menu doesn't switch it for you")
		}
		printLinkTestCaveats(udpBased)
		return

	case d.Type == "kcp" && loss < 1 && jitter < 20*time.Millisecond:
		fmt.Println("  " + bold("a TCP-family disguise (plain/noise/wss)"))
		fmt.Println("  • the link is clean and steady — KCP's always-on FEC is spending")
		fmt.Println("    bandwidth on error correction this link doesn't need right now")
		fmt.Println("  ! this is a disguise-type change too — see \"Manage tunnel\" → change")
		fmt.Println("    disguises on BOTH ends; this menu doesn't switch it for you")
		printLinkTestCaveats(udpBased)
		return
	}

	if cfg.Mode != "tcp" && cfg.Mode != "tcpmux" {
		// udp/kcp/quic already covered above, or the link is fine as-is.
		fmt.Println("  " + bold("keep the current transport") + " — nothing here suggests changing it")
		printLinkTestCaveats(udpBased)
		return
	}

	recommended := "tcp"
	why := "the link is clean and steady — a single connection has the least overhead"
	if loss > 0 || jitter > 15*time.Millisecond {
		recommended = "tcpmux"
		why = "even light loss/jitter costs one connection more than it costs many streams sharing a few sessions"
	}

	if cfg.Mode == recommended {
		fmt.Println("  " + bold("keep "+transportLabel(recommended, d.Type)) + " — already the right pick")
		fmt.Println("  • " + why)
		printLinkTestCaveats(udpBased)
		return
	}

	fmt.Println("  " + bold(transportLabel(recommended, d.Type)))
	fmt.Println("  • " + why)
	printLinkTestCaveats(udpBased)
	fmt.Println()
	fmt.Println(dim("Switching transport only works if the OTHER side switches too —"))
	fmt.Println(dim("until it does, the tunnel cannot reconnect."))
	fmt.Println()

	if confirm(fmt.Sprintf("Switch \"%s\" to %s now?", t.Name, transportLabel(recommended, d.Type)), false) {
		cfg.Mode = recommended
		if err := saveConfig(cfg, t.Path); err != nil {
			fmt.Println(red("failed to save: " + err.Error()))
			return
		}
		fmt.Println(green("saved."))
		fmt.Println()
		fmt.Println(yellow("⚠ change the peer's config to match, or this tunnel will not reconnect:"))
		fmt.Println(yellow("    mode = \"" + recommended + "\""))
		fmt.Println(yellow("  then restart the peer's rmtunnel-" + otherRole(t.Role) + "@<name> service."))
		fmt.Println()
		if confirm("restart "+t.unit()+" now to apply this side?", true) {
			run("systemctl", "restart", t.unit())
			fmt.Println(green("restarted."))
		}
	} else {
		fmt.Println(dim("left unchanged."))
	}
}

func printLinkTestCaveats(udpBased bool) {
	fmt.Println()
	if !udpBased {
		fmt.Println(dim("! this test only exercises this disguise's own path, so a clean result"))
		fmt.Println(dim("  here says nothing about whether UDP is throttled on this route — it"))
		fmt.Println(dim("  can't tell you whether KCP or QUIC would do any better or worse"))
	}
	fmt.Println(dim("! and it measures the link, not filtering: if the tunnel works but gets"))
	fmt.Println(dim("  throttled after a while, that's a filtering problem, and the answer is"))
	fmt.Println(dim("  a more disguised transport, not a faster one"))
}

func otherRole(role string) string {
	if role == "server" {
		return "client"
	}
	return "server"
}
