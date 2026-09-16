//go:build windows

package main

import "golang.org/x/sys/windows/svc"

// setupServiceLog gives the Windows service somewhere to log. A service has no
// stderr, so every diagnostic was being discarded — including "sidecar
// unavailable", the reason a remote session never starts.
func setupServiceLog() {
	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		return // interactive run: keep stderr
	}
	openAgentLog()
}
