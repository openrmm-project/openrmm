//go:build !windows

package main

import (
	"log"
	"os"
	"syscall"
)

// swapAndReload replaces the running binary and re-executes it in place. On Unix
// a running executable's file can be renamed (the running process keeps its open
// inode), so the swap is safe; syscall.Exec then replaces this process image with
// the new binary, keeping the same PID — so systemd/launchd (and the install.sh
// nohup fallback) all simply keep running the upgraded agent. Exec only returns
// on failure, in which case we roll the old binary back.
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
	_ = os.Remove(backup)

	log.Printf("update: re-executing %s", exe)
	args := append([]string{exe}, os.Args[1:]...)
	if err := syscall.Exec(exe, args, os.Environ()); err != nil {
		return err
	}
	return nil // unreachable on success
}
