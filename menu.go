package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

var stdin = bufio.NewReader(os.Stdin)

// readLine prints prompt and returns the next line. Input running out
// (stdin closed — piped from a script that ran dry, or Ctrl+D) is not
// something to paper over with an empty string and keep asking: without
// this check, every subsequent read returns "" immediately and a menu that
// insists on a non-empty answer, or a loop that reprompts on an invalid
// choice, spins forever at full speed instead of ever blocking again.
func readLine(prompt string) string {
	fmt.Print(prompt)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		if err == io.EOF {
			fmt.Println()
			fmt.Println(dim("input closed — exiting."))
			os.Exit(0)
		}
	}
	return strings.TrimSpace(line)
}

func readLineDefault(prompt, def string) string {
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]: ", prompt, dim(def))
	} else {
		prompt = prompt + ": "
	}
	v := readLine(prompt)
	if v == "" {
		return def
	}
	return v
}

func confirm(prompt string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	v := strings.ToLower(readLine(fmt.Sprintf("%s (%s): ", prompt, dim(hint))))
	if v == "" {
		return def
	}
	return v == "y" || v == "yes"
}

func pressEnter() {
	readLine(dim("press Enter to continue..."))
}

const bannerWidth = 54

func banner() string {
	top := purple("╭" + strings.Repeat("─", bannerWidth) + "╮")
	rule := purple("├" + strings.Repeat("─", bannerWidth) + "┤")
	bottom := purple("╰" + strings.Repeat("─", bannerWidth) + "╯")
	side := purple("│")

	title := gradientText(centerPad("R M   T U N N E L", bannerWidth))
	tagline := dim(centerPad("reverse tunnel · anti-censorship · self-tuning", bannerWidth))
	ver := bold(cyan(centerPad("v"+Version, bannerWidth)))
	repo := dim(centerPad(RepoURL, bannerWidth))
	author := dim(centerPad("by "+Author, bannerWidth))

	return strings.Join([]string{
		top,
		side + title + side,
		side + tagline + side,
		rule,
		side + ver + side,
		side + repo + side,
		side + author + side,
		bottom,
	}, "\n")
}

func centerPad(s string, width int) string {
	n := len([]rune(s))
	if n >= width {
		return s
	}
	left := (width - n) / 2
	right := width - n - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}

func runMenu() {
	startUpdateCheck()
	for {
		clearScreen()
		fmt.Println(banner())
		if notice := currentUpdateNotice(); notice != "" {
			fmt.Println()
			fmt.Println(noticeBox(notice))
		}
		fmt.Println()
		fmt.Println(bold(magenta("  Main Menu")))
		fmt.Println(gradientRule(40))
		fmt.Println(menuItem("1", "Build Iran tunnel "+dim("(server)")))
		fmt.Println(menuItem("2", "Build Kharej tunnel "+dim("(client)")))
		fmt.Println(menuItem("3", "Manage tunnels"))
		fmt.Println(menuItem("4", "Tune server "+dim("(OS optimization)")))
		fmt.Println(menuItem("5", "Speed & hardware benchmark"))
		fmt.Println(menuItem("6", "Update script"))
		fmt.Println(menuItem("7", "Uninstall"))
		fmt.Println(menuItem("0", "Exit"))
		fmt.Println(gradientRule(40))

		choice := readLine(bold(pink("choice ❯ ")))
		switch choice {
		case "1":
			wizardServer()
		case "2":
			wizardClient()
		case "3":
			menuManageTunnels()
		case "4":
			menuTune()
		case "5":
			menuBenchInteractive()
		case "6":
			menuUpdate()
		case "7":
			menuUninstall()
		case "0":
			fmt.Println(dim("bye."))
			return
		default:
			fmt.Println(red("invalid choice."))
			pressEnter()
		}
	}
}

// noticeBox wraps a warning line (currently just the update-available
// banner) in a small bordered box so it draws the eye without looking like
// an error — a plain "⚠ ..." line blended in with everything else above it.
func noticeBox(msg string) string {
	width := bannerWidth
	text := "⚠ " + msg
	runes := []rune(text)
	if len(runes) > width-2 {
		text = string(runes[:width-5]) + "..."
	}
	top := yellow("╭" + strings.Repeat("─", width) + "╮")
	bottom := yellow("╰" + strings.Repeat("─", width) + "╯")
	side := yellow("│")
	return top + "\n" + side + bold(yellow(centerPad(text, width))) + side + "\n" + bottom
}
