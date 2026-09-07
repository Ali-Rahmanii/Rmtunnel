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

func banner() string {
	line := purple("╔" + strings.Repeat("═", 52) + "╗")
	bottom := purple("╚" + strings.Repeat("═", 52) + "╝")
	title := blue(bold(centerPad("RM Tunnel", 52)))
	ver := cyan(centerPad("v"+Version, 52))
	repo := dim(centerPad(RepoURL, 52))
	author := dim(centerPad("by "+Author, 52))
	side := purple("║")
	return strings.Join([]string{
		line,
		side + title + side,
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
	for {
		clearScreen()
		fmt.Println(banner())
		fmt.Println()
		fmt.Println(bold(blue("Main Menu")))
		fmt.Println(purple(strings.Repeat("─", 40)))
		fmt.Println(menuItem("1", "Build Iran tunnel (server)"))
		fmt.Println(menuItem("2", "Build Kharej tunnel (client)"))
		fmt.Println(menuItem("3", "Manage tunnels"))
		fmt.Println(menuItem("4", "Tune server (OS optimization)"))
		fmt.Println(menuItem("5", "Speed & hardware benchmark"))
		fmt.Println(menuItem("6", "Update script"))
		fmt.Println(menuItem("7", "Uninstall"))
		fmt.Println(menuItem("0", "Exit"))
		fmt.Println(purple(strings.Repeat("─", 40)))

		choice := readLine(bold("choice: "))
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

func menuItem(key, label string) string {
	return "  " + bold(cyan(key)) + ")  " + label
}

func sectionHeader(title string) {
	clearScreen()
	fmt.Println(purple(bold(title)))
	fmt.Println(purple(strings.Repeat("─", len([]rune(title))+4)))
	fmt.Println()
}
