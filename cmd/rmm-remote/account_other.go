//go:build !linux && !windows

package main

// sessionAccount is the person at the machine. On macOS the agent starts the
// sidecar as the console user, so that is the process's own account.
func sessionAccount() string { return processAccount() }
