//go:build !windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
)

// swapAndReload replaces the running binary and re-executes it in place. On Unix
// a running executable's file can be renamed (the running process keeps its open
// inode), so the swap is safe; syscall.Exec then replaces this process image with
// the new binary, keeping the same PID — so systemd/launchd (and the install.sh
// nohup fallback) all simply keep running the upgraded agent. Exec only returns
// on failure, in which case we roll the old binary back.
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

	log.Printf("update: re-executing %s", exe)
	args := append([]string{exe}, os.Args[1:]...)
	if err := syscall.Exec(exe, args, os.Environ()); err != nil {
		if rollbackErr := rollbackAndReload(exe); rollbackErr != nil {
			return fmt.Errorf("exec update: %v; rollback: %w", err, rollbackErr)
		}
		return err
	}
	return nil // unreachable on success
}

func rollbackAndReload(exe string) error {
	if err := restorePreviousBinary(exe); err != nil {
		return err
	}
	args := append([]string{exe}, os.Args[1:]...)
	return syscall.Exec(exe, args, os.Environ())
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
	return exec.Command(exe, "-action", "restart").Start()
}
