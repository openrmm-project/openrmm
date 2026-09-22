//go:build !windows && !darwin

package main

import (
	"os"
	"os/exec"
)

// launchSidecar starts the media sidecar as a child process. On non-Windows the
// agent typically isn't a session-0 service with the desktop-isolation problem,
// so a plain exec is enough.
func launchSidecar(bin, relay, ticket string, extra ...string) error {
	cmd := exec.Command(bin, sidecarArgs(relay, ticket, extra)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
