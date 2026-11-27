package main

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestRebootStatusUnix(t *testing.T) {
	for _, tc := range []struct {
		name, goos, release string
		marker              error
		want                string
	}{
		{"Debian missing", "linux", "ID=debian", fs.ErrNotExist, protocol.RebootNotRequired},
		{"Ubuntu derivative", "linux", "ID=custom\nID_LIKE=\"ubuntu debian\"", fs.ErrNotExist, protocol.RebootNotRequired},
		{"unsupported Linux", "linux", "ID=fedora", fs.ErrNotExist, protocol.RebootUnknown},
		{"marker access denied", "linux", "ID=debian", fs.ErrPermission, protocol.RebootUnknown},
		{"positive marker", "linux", "ID=debian", nil, protocol.RebootRequired},
		{"positive unknown distro", "linux", "ID=fedora", nil, protocol.RebootRequired},
		{"macOS", "darwin", "", fs.ErrNotExist, protocol.RebootUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				switch path {
				case "/etc/os-release":
					return []byte(tc.release), nil
				case "/proc/sys/kernel/random/boot_id":
					return []byte("boot-one\n"), nil
				case pendingRebootFile + ".pkgs":
					return []byte("linux-image-1\nlibc6\n"), nil
				}
				return nil, fs.ErrNotExist
			}
			r := rebootStatusUnix(tc.goos, read, func(string) (fs.FileInfo, error) { return nil, tc.marker })
			if r.Status != tc.want {
				t.Fatalf("status=%s want %s", r.Status, tc.want)
			}
			if tc.goos == "linux" && r.BootID != "boot-one" {
				t.Fatalf("boot ID=%q", r.BootID)
			}
			if r.Status == protocol.RebootRequired && r.Reason != "linux-image-1, libc6" {
				t.Fatalf("reason=%q", r.Reason)
			}
		})
	}
	read := func(path string) ([]byte, error) {
		if strings.HasSuffix(path, ".pkgs") {
			return []byte(strings.Repeat("x", 3000)), nil
		}
		return nil, fs.ErrPermission
	}
	r := rebootStatusUnix("linux", read, func(string) (fs.FileInfo, error) { return nil, nil })
	if r.Status != protocol.RebootRequired || r.Reason != pendingRebootFile || r.BootID != "" {
		t.Fatalf("unbounded/inaccessible optional data: %+v", r)
	}
	r = rebootStatusUnix("linux", read, func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist })
	if r.Status != protocol.RebootUnknown {
		t.Fatalf("unreadable OS identified as healthy: %+v", r)
	}
}

func TestRebootStatusWindows(t *testing.T) {
	denied := errors.New("access denied")
	for _, tc := range []struct {
		name    string
		markers []bool
		errs    []error
		want    string
	}{
		{"no markers", []bool{false, false, false}, []error{nil, nil, nil}, protocol.RebootNotRequired},
		{"CBS", []bool{true, false, false}, []error{nil, nil, nil}, protocol.RebootRequired},
		{"file renames", []bool{false, false, true}, []error{nil, nil, nil}, protocol.RebootRequired},
		{"access denied", []bool{false, false, false}, []error{denied, nil, nil}, protocol.RebootUnknown},
		{"positive despite earlier error", []bool{false, true, false}, []error{denied, nil, nil}, protocol.RebootRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := 0
			r := rebootStatusWindows(func(string, string) (bool, error) {
				present, err := tc.markers[i], tc.errs[i]
				i++
				return present, err
			})
			if r.Status != tc.want {
				t.Fatalf("status=%s want %s", r.Status, tc.want)
			}
		})
	}
}

func TestLegacyRebootQueryContract(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   any
	}{{protocol.RebootRequired, true}, {protocol.RebootNotRequired, false}, {protocol.RebootUnknown, "unknown"}} {
		got, why := legacyRebootStatus(&protocol.RebootStatus{Status: tc.status, Reason: "reason"})
		if got != tc.want || why != "reason" {
			t.Fatalf("legacy(%s)=%v,%q", tc.status, got, why)
		}
	}
}
