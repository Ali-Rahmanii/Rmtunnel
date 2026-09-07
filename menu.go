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
			fmt.Println(dim("ورودی تموم شد — خروج."))
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
	return v == "y" || v == "yes" || v == "بله" || v == "آره"
}

func pressEnter() {
	readLine(dim("برای ادامه Enter رو بزن..."))
}

func banner() string {
	line := purple("╔" + strings.Repeat("═", 52) + "╗")
	bottom := purple("╚" + strings.Repeat("═", 52) + "╝")
	title := blue(bold(centerPad("RM Tunnel", 52)))
	ver := cyan(centerPad("نسخه "+Version, 52))
	repo := dim(centerPad(RepoURL, 52))
	author := dim(centerPad("توسعه‌دهنده: "+Author, 52))
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
		fmt.Println(bold(blue("منوی اصلی")))
		fmt.Println(purple(strings.Repeat("─", 40)))
		fmt.Println(menuItem("1", "ساخت تانل ایران (سرور)"))
		fmt.Println(menuItem("2", "ساخت تانل خارج (کلاینت)"))
		fmt.Println(menuItem("3", "تیون سرور (بهینه‌سازی سیستم‌عامل)"))
		fmt.Println(menuItem("4", "بنچمارک سرعت و سخت‌افزار"))
		fmt.Println(menuItem("5", "وضعیت سرویس‌ها"))
		fmt.Println(menuItem("6", "آپدیت اسکریپت"))
		fmt.Println(menuItem("7", "حذف نصب"))
		fmt.Println(menuItem("0", "خروج"))
		fmt.Println(purple(strings.Repeat("─", 40)))

		choice := readLine(bold("انتخابت: "))
		switch choice {
		case "1":
			wizardServer()
		case "2":
			wizardClient()
		case "3":
			menuTune()
		case "4":
			menuBenchInteractive()
		case "5":
			menuStatus()
		case "6":
			menuUpdate()
		case "7":
			menuUninstall()
		case "0":
			fmt.Println(dim("خداحافظ."))
			return
		default:
			fmt.Println(red("گزینه‌ی نامعتبر."))
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
