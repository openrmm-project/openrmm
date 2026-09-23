//go:build darwin

package main

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// platformDetail names the OS release for the startup line: privacy prompts
// and their quirks change between macOS versions, so a log without it cannot
// be compared with another Mac's.
func platformDetail() string {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil || v == "" {
		v = "unknown version"
	}
	return "macOS " + v + " " + runtime.GOARCH
}
