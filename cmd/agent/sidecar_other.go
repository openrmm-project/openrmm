//go:build !darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// sidecarDownloadName is the artifact published under /dl/ for this platform.
func sidecarDownloadName() string {
	name := fmt.Sprintf("rmm-remote-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// sidecarExePath is the executable the agent launches, next to its own binary.
func sidecarExePath(agentDir string) string {
	return filepath.Join(agentDir, sidecarName())
}

// stageSidecar prepares a downloaded artifact for validation. Everywhere except
// macOS the artifact is the executable itself, so there is nothing to unpack.
func stageSidecar(artifact string) (exe string, commit func(dest string) error, cleanup func(), err error) {
	if err := os.Chmod(artifact, 0o755); err != nil && runtime.GOOS != "windows" {
		return "", nil, nil, err
	}
	return artifact,
		func(dest string) error { return os.Rename(artifact, dest) },
		func() { _ = os.Remove(artifact) },
		nil
}
