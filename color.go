package main

import "os"

// A small blue/purple theme for the interactive menu. Plain ANSI SGR codes —
// no library needed, and every terminal this project's actual audience uses
// (a Linux server over SSH, Windows Terminal, a modern Linux desktop)
// supports them. Setting NO_COLOR (the informal but widely respected
// convention) turns all of this into a no-op.
var colorEnabled = os.Getenv("NO_COLOR") == ""

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiBlue   = "\033[38;5;69m"
	ansiPurple = "\033[38;5;135m"
	ansiCyan   = "\033[38;5;80m"
	ansiGreen  = "\033[38;5;78m"
	ansiRed    = "\033[38;5;203m"
	ansiYellow = "\033[38;5;221m"
)

func paint(code, s string) string {
	if !colorEnabled {
		return s
	}
	return code + s + ansiReset
}

func blue(s string) string   { return paint(ansiBlue, s) }
func purple(s string) string { return paint(ansiPurple, s) }
func cyan(s string) string   { return paint(ansiCyan, s) }
func green(s string) string  { return paint(ansiGreen, s) }
func red(s string) string    { return paint(ansiRed, s) }
func yellow(s string) string { return paint(ansiYellow, s) }
func bold(s string) string   { return paint(ansiBold, s) }
func dim(s string) string    { return paint(ansiDim, s) }

func clearScreen() {
	if colorEnabled {
		print("\033[H\033[2J")
	}
}
