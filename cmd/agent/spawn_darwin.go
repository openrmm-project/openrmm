//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

func launchSidecar(bin, relay, ticket string, extra ...string) error {
	args := sidecarArgs(relay, ticket, extra)
	filesOnly := false
	for _, arg := range extra {
		if arg == "-files" {
			filesOnly = true
		}
	}
	cmd := exec.Command(bin, args...)
	if os.Geteuid() == 0 && !filesOnly {
		// A LaunchDaemon has no Aqua session. Enter the logged-in user's
		// bootstrap namespace AND identity; root does not bypass Apple's TCC.
		info, err := os.Stat("/dev/console")
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid == 0 {
			return fmt.Errorf("macOS remote desktop requires a logged-in console user")
		}
		uid := strconv.FormatUint(uint64(stat.Uid), 10)
		account, err := user.LookupId(uid)
		if err != nil {
			return err
		}
		launchArgs := []string{"asuser", uid, "/usr/bin/sudo", "-n", "-H", "-u", "#" + uid, "--", bin}
		cmd = exec.Command("/bin/launchctl", append(launchArgs, args...)...)
		cmd.Dir = account.HomeDir
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
