package main

import "fmt"

// A wizard (building or editing a tunnel) is a long chain of numbered
// questions with no way to back out except Ctrl-C — which on some
// terminals kills more than just the current prompt. askChoice/askMenu
// make every numbered question in a wizard accept "0" as "cancel and
// return to the main menu," caught cleanly by runWizard wrapping the
// wizard's entry point, instead of "0" being silently treated as just
// another unmatched value that falls through to a question's default.

type wizardCancelled struct{}

// runWizard runs fn, catching a "0" cancel from any askChoice/askMenu call
// within it (however deep) and returning cleanly instead of continuing the
// wizard or crashing. Every wizard entry point reachable from the main menu
// wraps its body in this.
func runWizard(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(wizardCancelled); ok {
				fmt.Println(dim("cancelled — back to the main menu."))
				pressEnter()
				return
			}
			panic(r) // not a cancel — a real bug, let it surface normally
		}
	}()
	fn()
}

// askChoice is readLineDefault for a numbered-menu question: identical
// behavior, except "0" cancels the whole wizard via runWizard instead of
// being passed back as a literal (and silently mismatching every case,
// which would otherwise fall through to whatever the caller treats as
// default — a confusing way for "0" to actually mean "keep going anyway").
func askChoice(prompt, def string) string {
	c := readLineDefault(prompt, def)
	if c == "0" {
		panic(wizardCancelled{})
	}
	return c
}
