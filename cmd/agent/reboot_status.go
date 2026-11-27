package main

import (
	"errors"
	"io/fs"
	"strings"

	"github.com/p1ngu/rmm/internal/protocol"
)

const pendingRebootFile = "/var/run/reboot-required"

func legacyRebootStatus(r *protocol.RebootStatus) (any, string) {
	switch r.Status {
	case protocol.RebootRequired:
		return true, r.Reason
	case protocol.RebootNotRequired:
		return false, r.Reason
	default:
		return "unknown", r.Reason
	}
}

// Debian's marker is not universal across Linux distributions. Missing data
// on an unsupported OS or an inaccessible marker is not a healthy result.
func rebootStatusUnix(goos string, readFile func(string) ([]byte, error), stat func(string) (fs.FileInfo, error)) *protocol.RebootStatus {
	r := &protocol.RebootStatus{Status: protocol.RebootUnknown, Reason: "unsupported operating system"}
	if goos != "linux" {
		return r
	}
	if boot, err := readFile("/proc/sys/kernel/random/boot_id"); err == nil && len(boot) <= 128 {
		r.BootID = strings.TrimSpace(string(boot))
	}
	_, err := stat(pendingRebootFile)
	if err == nil {
		r.Status = protocol.RebootRequired
		r.Reason = pendingRebootFile
		// Names identify the reason; bound the content before it enters telemetry.
		if packages, e := readFile(pendingRebootFile + ".pkgs"); e == nil && len(packages) <= 1024 {
			if names := strings.Join(strings.Fields(string(packages)), ", "); names != "" {
				r.Reason = names
			}
		}
		return r
	}
	if !errors.Is(err, fs.ErrNotExist) {
		r.Reason = "reboot marker could not be read"
		return r
	}
	osRelease, err := readFile("/etc/os-release")
	if err != nil {
		r.Reason = "operating system could not be identified"
		return r
	}
	for _, line := range strings.Split(string(osRelease), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || (k != "ID" && k != "ID_LIKE") {
			continue
		}
		for _, id := range strings.Fields(strings.Trim(v, "\"'")) {
			if id == "debian" || id == "ubuntu" {
				r.Status = protocol.RebootNotRequired
				r.Reason = ""
				return r
			}
		}
	}
	r.Reason = "reboot detection is not supported on this Linux distribution"
	return r
}

// Readers distinguish missing Windows registry markers from access errors.
// A positive marker wins over an earlier error; otherwise ambiguity is unknown.
func rebootStatusWindows(read func(string, string) (bool, error)) *protocol.RebootStatus {
	r := &protocol.RebootStatus{Status: protocol.RebootNotRequired}
	for _, marker := range []struct{ path, value, reason string }{
		{`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`, "", "component servicing"},
		{`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`, "", "Windows Update"},
		{`SYSTEM\CurrentControlSet\Control\Session Manager`, "PendingFileRenameOperations", "file renames queued for the next boot"},
	} {
		present, err := read(marker.path, marker.value)
		if present {
			return &protocol.RebootStatus{Status: protocol.RebootRequired, Reason: marker.reason}
		}
		if err != nil {
			r.Status = protocol.RebootUnknown
			r.Reason = "reboot registry markers could not be read"
		}
	}
	return r
}
