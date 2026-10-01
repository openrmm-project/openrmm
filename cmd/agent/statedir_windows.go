//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// stateDirSDDL is the only access the agent directory should carry: SYSTEM and
// Administrators, inherited by every file in it, nothing from the parent.
// state.json holds the agent's bearer token and the outboxes hold job output
// that has not been delivered yet, so a standard user who can read them can
// impersonate this device to the server.
const stateDirSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// hardenStateDir repairs the directory ACL on every start. The installer sets
// it, but agents update themselves without running the installer again, so an
// endpoint installed by an older one (which granted BUILTIN\Users read access)
// would otherwise keep that grant forever.
//
// It acts only on the installed layout — the state file next to the running
// executable — and only as LocalSystem, so a developer running the agent from
// their own folder is never locked out of it.
func hardenStateDir(statePath string) {
	dir := filepath.Dir(statePath)
	exe, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Clean(filepath.Dir(exe)), filepath.Clean(dir)) {
		return
	}
	if !runningAsLocalSystem() {
		return
	}
	if err := restrictAgentDir(dir); err != nil {
		log.Printf("state dir: %v", err)
	}
}

// restrictAgentDir writes stateDirSDDL onto dir. Setting a DACL through
// SetNamedSecurityInfo propagates its inheritable entries to the existing
// files, which is what removes the old grant from a state.json written before
// this ran. Whoever owns the directory may rewrite its ACL; the installer takes
// ownership too, so failing to repair the owner here is logged, not returned.
func restrictAgentDir(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(stateDirSDDL)
	if err != nil {
		return fmt.Errorf("build descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read descriptor: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("restrict %s: %w", dir, err)
	}
	if owner, _, err := sd.Owner(); err == nil {
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION, owner, nil, nil, nil); err != nil {
			log.Printf("state dir: take ownership of %s: %v", dir, err)
		}
	}
	return nil
}

func runningAsLocalSystem() bool {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	return user.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}
