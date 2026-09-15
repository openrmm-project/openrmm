//go:build !windows

package main

// setupServiceLog is a no-op away from Windows: systemd and launchd already
// capture the daemon's stderr.
func setupServiceLog() {}
