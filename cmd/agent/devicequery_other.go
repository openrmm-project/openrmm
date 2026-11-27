//go:build !windows

package main

import (
	"io"
	"os"
	"runtime"

	"github.com/p1ngu/rmm/internal/protocol"
)

func collectRebootStatus() *protocol.RebootStatus {
	return rebootStatusUnix(runtime.GOOS, readRebootFile, os.Stat)
}

func pendingReboot() (any, string) { return legacyRebootStatus(collectRebootStatus()) }

// Marker/reason files are small. A corrupt file must not turn heartbeat
// telemetry into an unbounded allocation.
func readRebootFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, 2049))
}
