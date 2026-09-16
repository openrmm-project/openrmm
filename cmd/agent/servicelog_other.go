//go:build !windows && !darwin

package main

// setupServiceLog is a no-op on Linux: systemd already captures the unit's
// stderr into the journal.
func setupServiceLog() {}
