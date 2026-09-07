package main

import (
	"fmt"
	"os"
	"strings"
)

// A blue/purple/pink theme for the interactive menu. Plain ANSI SGR codes —
// no library needed, and every terminal this project's actual audience uses
// (a Linux server over SSH, Windows Terminal, a modern Linux desktop)
// supports them. Setting NO_COLOR (the informal but widely respected
// convention) turns all of this into a no-op.
var colorEnabled = os.Getenv("NO_COLOR") == ""

const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"

	ansiBlue    = "\033[38;5;69m"
	ansiPurple  = "\033[38;5;135m"
	ansiCyan    = "\033[38;5;80m"
	ansiGreen   = "\033[38;5;78m"
	ansiRed     = "\033[38;5;203m"
	ansiYellow  = "\033[38;5;221m"
	ansiMagenta = "\033[38;5;171m"
	ansiPink    = "\033[38;5;212m"
	ansiGray    = "\033[38;5;244m"
)

func paint(code, s string) string {
	if !colorEnabled {
		return s
	}
	return code + s + ansiReset
}

func blue(s string) string    { return paint(ansiBlue, s) }
func purple(s string) string  { return paint(ansiPurple, s) }
func cyan(s string) string    { return paint(ansiCyan, s) }
func green(s string) string   { return paint(ansiGreen, s) }
func red(s string) string     { return paint(ansiRed, s) }
func yellow(s string) string  { return paint(ansiYellow, s) }
func magenta(s string) string { return paint(ansiMagenta, s) }
func pink(s string) string    { return paint(ansiPink, s) }
func gray(s string) string    { return paint(ansiGray, s) }
func bold(s string) string    { return paint(ansiBold, s) }
func dim(s string) string     { return paint(ansiDim, s) }

func clearScreen() {
	if colorEnabled {
		print("\033[H\033[2J")
	}
}

// gradientPalette is a blue -> purple -> pink sweep, used to color text
// character-by-character. 256-color codes rather than 24-bit truecolor, so
// the effect still shows up correctly over a plain SSH session, not just a
// modern local terminal.
var gradientPalette = []string{
	"\033[38;5;69m",  // blue
	"\033[38;5;75m",  // blue-cyan
	"\033[38;5;111m", // light blue
	"\033[38;5;135m", // purple
	"\033[38;5;171m", // magenta
	"\033[38;5;212m", // pink
}

// gradientText colors s one rune at a time, sweeping through gradientPalette
// — used for the banner title so it reads as a single deliberate wordmark
// instead of a flat block of one color.
func gradientText(s string) string {
	if !colorEnabled {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	n := len(gradientPalette)
	for i, r := range runes {
		if r == ' ' {
			b.WriteRune(r)
			continue
		}
		b.WriteString(gradientPalette[i%n])
		b.WriteRune(r)
		b.WriteString(ansiReset)
	}
	return b.String()
}

// gradientRule draws a horizontal divider that sweeps the same palette, so
// section breaks feel like part of the same theme as the banner instead of a
// plain dashed line.
func gradientRule(width int) string {
	if !colorEnabled {
		return strings.Repeat("─", width)
	}
	n := len(gradientPalette)
	var b strings.Builder
	for i := 0; i < width; i++ {
		b.WriteString(gradientPalette[i%n])
		b.WriteRune('─')
	}
	b.WriteString(ansiReset)
	return b.String()
}

// statusBadge is the one place "is this tunnel/service up" gets rendered, so
// every screen that shows it (the tunnel list, the per-tunnel action menu)
// looks identical.
func statusBadge(active bool) string {
	if active {
		return bold(green("● active"))
	}
	return bold(red("● inactive"))
}

// roleTag colors "server"/"client" consistently everywhere a tunnel's role
// is shown — blue for the Iran/server side, purple for the Kharej/client
// side, matching the banner's own two-tone theme. Trims before comparing so
// a caller can pass an already width-padded role (padding has to happen on
// the plain string, before coloring, or the padding counts the escape
// bytes too — see tunnels.go's list) without it silently falling through to
// "client".
func roleTag(role string) string {
	if strings.TrimSpace(role) == "server" {
		return blue(bold(role))
	}
	return purple(bold(role))
}

func menuItem(key, label string) string {
	return "  " + purple("❯") + " " + bold(cyan("["+key+"]")) + "  " + label
}

func sectionHeader(title string) {
	clearScreen()
	fmt.Println(purple("▸ ") + bold(cyan(title)))
	fmt.Println(gradientRule(56))
	fmt.Println()
}
