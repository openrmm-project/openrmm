//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// macOS ships the sidecar as a signed application bundle, not a bare binary.
// Privacy grants — Screen Recording, Accessibility, Local Network — are keyed to
// a bundle; a loose executable is never offered to the operator to allow, so a
// LAN-hosted server cannot be reached at all. The bundle lives outside the
// agent's own directory because it is an application, not a helper binary.
const macSidecarBundle = "/Library/Application Support/OpenRMM/RMMRemote.app"

func sidecarDownloadName() string {
	return fmt.Sprintf("rmm-remote-darwin-%s.app.tar.gz", runtime.GOARCH)
}

func sidecarExePath(string) string {
	return filepath.Join(macSidecarBundle, "Contents", "MacOS", "rmm-remote")
}

// stageSidecar unpacks the archive next to it so the candidate can be probed
// before anything replaces the bundle in use.
func stageSidecar(artifact string) (string, func(string) error, func(), error) {
	stage := artifact + ".stage"
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", nil, nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(stage)
		_ = os.Remove(artifact)
	}
	// --strip-components keeps the archive's own top-level name from mattering.
	// --no-same-owner: the archive carries the CI runner's numeric uid, and
	// restoring it would leave the bundle owned by whichever local account
	// happens to share that number.
	cmd := exec.Command("/usr/bin/tar", "-xzf", artifact, "-C", stage, "--strip-components", "1", "--no-same-owner")
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("unpack sidecar bundle: %w: %s", err, out)
	}
	exe := filepath.Join(stage, "Contents", "MacOS", "rmm-remote")
	if _, err := os.Stat(exe); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("sidecar bundle has no %s", exe)
	}
	commit := func(dest string) error {
		// dest is the executable inside the bundle; install the bundle itself.
		bundle := filepath.Dir(filepath.Dir(filepath.Dir(dest)))
		if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
			return err
		}
		// Two renames rather than one: a directory cannot be renamed over a
		// populated directory. The old bundle is moved aside first so a failure
		// leaves something to fall back to rather than nothing.
		old := bundle + ".old"
		_ = os.RemoveAll(old)
		if _, err := os.Stat(bundle); err == nil {
			if err := os.Rename(bundle, old); err != nil {
				return err
			}
		}
		if err := os.Rename(stage, bundle); err != nil {
			_ = os.Rename(old, bundle) // put the working one back
			return err
		}
		_ = os.RemoveAll(old)
		_ = os.Remove(artifact)
		return nil
	}
	return exe, commit, cleanup, nil
}
