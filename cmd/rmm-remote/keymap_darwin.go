//go:build darwin

package main

// browserKeyMac maps DOM keyCode values (what the viewer sends as {t:"k",code})
// to macOS virtual key codes (kVK_*), US-ANSI layout, best effort. Built once at
// package init. Validate punctuation/layout-specific keys on a real Mac.
var browserKeyMac = buildMacKeymap()

func buildMacKeymap() map[int]int {
	m := map[int]int{}

	// Letters A–Z (browser 65–90) → kVK_ANSI_* in alphabetical order.
	letters := []int{
		0, 11, 8, 2, 14, 3, 5, 4, 34, 38, 40, 37, 46, // A..M
		45, 31, 35, 12, 15, 1, 17, 32, 9, 13, 7, 16, 6, // N..Z
	}
	for i, kc := range letters {
		m[65+i] = kc
	}

	// Digits 0–9 (browser 48–57) → top-row number keys.
	digits := []int{29, 18, 19, 20, 21, 23, 22, 26, 28, 25}
	for i, kc := range digits {
		m[48+i] = kc
	}

	// Control / navigation / punctuation.
	for k, v := range map[int]int{
		8:   51,  // Backspace → Delete
		9:   48,  // Tab
		13:  36,  // Enter → Return
		16:  56,  // Shift
		17:  59,  // Control
		18:  58,  // Alt → Option
		20:  57,  // CapsLock
		27:  53,  // Escape
		32:  49,  // Space
		33:  116, // PageUp
		34:  121, // PageDown
		35:  119, // End
		36:  115, // Home
		37:  123, // ArrowLeft
		38:  126, // ArrowUp
		39:  124, // ArrowRight
		40:  125, // ArrowDown
		46:  117, // Delete → ForwardDelete
		91:  55,  // Meta → Command
		186: 41,  // ;
		187: 24,  // =
		188: 43,  // ,
		189: 27,  // -
		190: 47,  // .
		191: 44,  // /
		192: 50,  // `
		219: 33,  // [
		220: 42,  // \
		221: 30,  // ]
		222: 39,  // '
	} {
		m[k] = v
	}
	return m
}
