//go:build windows

package main

import (
	"fmt"
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
func swapAndReload(exe, tmp string) error {
	backup := exe + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		_ = os.Remove(updatePendingPath(exe))
		return err
	}
	if err := os.Rename(exe+".sig", backup+".sig"); err != nil {
		_ = os.Rename(backup, exe)
		_ = os.Remove(updatePendingPath(exe))
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(backup, exe) // restore
		_ = os.Rename(backup+".sig", exe+".sig")
		_ = os.Remove(updatePendingPath(exe))
		return err
	}
	if err := os.Rename(tmp+".sig", exe+".sig"); err != nil {
		_ = os.Rename(exe, tmp)
		_ = os.Rename(backup, exe)
		_ = os.Rename(backup+".sig", exe+".sig")
		_ = os.Remove(updatePendingPath(exe))
		return err
	}

	log.Printf("update: scheduling service restart to load %s", exe)
	cmd := exec.Command(exe, "-action", "restart")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup | createBreakawayFromJob,
	}
	if err := cmd.Start(); err != nil {
		if rollbackErr := rollbackAndReload(exe); rollbackErr != nil {
			return fmt.Errorf("schedule restart: %v; rollback: %w", err, rollbackErr)
		}
		return err
	}
	return nil
}

func rollbackAndReload(exe string) error {
	if err := restorePreviousBinary(exe); err != nil {
		return err
	}
	cmd := exec.Command(exe, "-action", "restart")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup | createBreakawayFromJob,
	}
	return cmd.Start()
}

func restorePreviousBinary(exe string) error {
	failed := exe + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(exe, failed); err != nil {
		return err
	}
	if err := os.Rename(exe+".old", exe); err != nil {
		_ = os.Rename(failed, exe)
		return err
	}
	_ = os.Remove(exe + ".sig")
	if err := os.Rename(exe+".old.sig", exe+".sig"); err != nil {
		return err
	}
	_ = os.Remove(updatePendingPath(exe))
	return nil
}

func rollbackAndRestartService(exe string) error {
	if err := restorePreviousBinary(exe); err != nil {
		return err
	}
	cmd := exec.Command(exe, "-action", "restart")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup | createBreakawayFromJob,
	}
	return cmd.Start()
}
