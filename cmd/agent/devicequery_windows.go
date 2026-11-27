//go:build windows

package main

import (
	"errors"
	"github.com/p1ngu/rmm/internal/protocol"
	"golang.org/x/sys/windows/registry"
)

func collectRebootStatus() *protocol.RebootStatus {
	return rebootStatusWindows(func(path, value string) (bool, error) {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		defer key.Close()
		if value == "" {
			return true, nil
		}
		values, _, err := key.GetStringsValue(value)
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return len(values) > 0, nil
	})
}

func pendingReboot() (any, string) { return legacyRebootStatus(collectRebootStatus()) }
