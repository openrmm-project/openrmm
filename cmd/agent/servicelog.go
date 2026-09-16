package main

import (
	"log"
	"os"
	"path/filepath"
)

// maxAgentLogBytes rotates once rather than growing without bound; the agent
// runs for months at a time.
const maxAgentLogBytes = 4 << 20

// openAgentLog points the logger at a file next to the binary. A service has
// nowhere else to write: Windows gives it no stderr, and launchd sends stdout
// to /dev/null unless the job declares a path. Without it every log.Printf —
// including the reason a remote session never started — is discarded.
func openAgentLog() {
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
