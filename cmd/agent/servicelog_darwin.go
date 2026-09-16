//go:build darwin

package main

import "github.com/kardianos/service"

// setupServiceLog does for launchd what the Windows build does for services.
// The generated job declares no StandardOutPath, so the daemon's output goes to
// /dev/null; a remote session that fails to start its sidecar left no trace
// anywhere, on either side of the relay.
func setupServiceLog() {
	if service.Interactive() {
		return // run from a terminal: keep stderr
	}
	openAgentLog()
}
