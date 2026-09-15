//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
)

// maxAgentLogBytes rotates once rather than growing without bound; the agent
// runs for months at a time.
const maxAgentLogBytes = 4 << 20

// setupServiceLog gives the Windows service somewhere to log. A service has no
// stderr, so every log.Printf — including "sidecar unavailable", the reason a
// remote session never starts — was discarded, leaving the endpoint with no
// record of why it failed. Other platforms already have one: systemd captures
// the journal and launchd the daemon's output.
func setupServiceLog() {
	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		return // interactive run: keep stderr
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	path := filepath.Join(filepath.Dir(exe), "agent.log")
	if info, err := os.Stat(path); err == nil && info.Size() >= maxAgentLogBytes {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(f)
}
