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

// bigFont is a small 5x5 block font — just enough letters to spell out
// "RM TUNNEL" as a large wordmark in the banner, the same idea as figlet's
// "block" font but built by hand for exactly the letters needed. '#' is a
// filled cell, '.' is blank; bigBannerLines converts both before coloring.
var bigFont = map[rune][5]string{
	'R': {"####.", "#...#", "####.", "#..#.", "#...#"},
	'M': {"#...#", "##.##", "#.#.#", "#...#", "#...#"},
	'T': {"#####", "..#..", "..#..", "..#..", "..#.."},
	'U': {"#...#", "#...#", "#...#", "#...#", ".###."},
	'N': {"#...#", "##..#", "#.#.#", "#..##", "#...#"},
	'E': {"#####", "#....", "####.", "#....", "#####"},
	'L': {"#....", "#....", "#....", "#....", "#####"},
	' ': {"..", "..", "..", "..", ".."},
}

// bigBannerLines renders s (upper-case letters and spaces only) as 5 lines
// of large block characters, centered to width and colored one gradient
// step per row so the wordmark itself carries the same blue-to-pink theme
// as the rest of the panel.
func bigBannerLines(s string, width int) []string {
	rowColors := []func(string) string{blue, blue, purple, magenta, pink}
	raw := make([]strings.Builder, 5)
	runes := []rune(s)
	for i, r := range runes {
		glyph, ok := bigFont[r]
		if !ok {
			glyph = bigFont[' ']
		}
		for row := 0; row < 5; row++ {
			raw[row].WriteString(glyph[row])
			if i < len(runes)-1 {
				raw[row].WriteByte(' ')
			}
		}
	}
	out := make([]string, 5)
	for row := 0; row < 5; row++ {
		line := strings.ReplaceAll(raw[row].String(), ".", " ")
		line = centerPad(line, width)
		line = strings.ReplaceAll(line, "#", "█")
		out[row] = rowColors[row](line)
	}
	return out
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

// roleTag colors a tunnelRef.Role consistently everywhere one is shown —
// blue for the Iran/server side, purple for the Kharej/client side, cyan
// for a paqet tunnel (a distinct engine — see paqet.go), matching the
// banner's own theme. Trims before comparing so a caller can pass an
// already width-padded role (padding has to happen on the plain string,
// before coloring, or the padding counts the escape bytes too — see
// tunnels.go's list) without it misclassifying.
func roleTag(role string) string {
	switch strings.TrimSpace(role) {
	case "server":
		return blue(bold(role))
	case "client":
		return purple(bold(role))
	default: // paqet-server, paqet-client
		return cyan(bold(role))
	}
}

func menuItem(key, label string) string {
	return "  " + purple("❯") + " " + bold(cyan("["+key+"]")) + "  " + label
}

func sectionHeader(title string) {
	clearScreen()
	fmt.Println(purple("▸ ") + bold(cyan(title)))
	fmt.Println(gradientRule(bannerWidth))
	fmt.Println()
}
