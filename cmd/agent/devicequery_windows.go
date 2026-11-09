//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// pendingReboot reads the three places Windows records that it needs a
// restart: component servicing, Windows Update, and file renames queued for
// the next boot.
func pendingReboot() (any, string) {
	for _, k := range []struct{ path, why string }{
		{`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`, "component servicing"},
		{`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`, "Windows Update"},
	} {
		if key, err := registry.OpenKey(registry.LOCAL_MACHINE, k.path, registry.QUERY_VALUE); err == nil {
			key.Close()
			return true, k.why
		}
	}
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager`, registry.QUERY_VALUE); err == nil {
		defer key.Close()
		if v, _, err := key.GetStringsValue("PendingFileRenameOperations"); err == nil && len(v) > 0 {
			return true, "file renames queued for the next boot"
		}
	}
	return false, ""
}
