//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"syscall"
)

// Windows process-creation flags (avoid depending on exact x/sys symbol names).
const (
	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
)

// swapAndReload stages the new binary and triggers a service restart. Windows
// can neither overwrite nor exec a running image, but it CAN rename a running
// .exe — so we move the live binary aside, drop the new one in place, then launch
// a detached helper (the new binary, "-action restart") that stops the current
// service process and lets the SCM relaunch the upgraded one. The old binary is
// removed on the next startup (cleanupOldBinary).
func swapAndReload(exe, tmp, etagPath, etag string) error {
	backup := exe + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(backup, exe) // restore
		return err
	}
	_ = os.WriteFile(etagPath, []byte(etag), 0o644)

	log.Printf("update: scheduling service restart to load %s", exe)
	cmd := exec.Command(exe, "-action", "restart")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup | createBreakawayFromJob,
	}
	return cmd.Start()
}
