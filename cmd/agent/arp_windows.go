//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// The uninstaller ships inside the agent rather than being written by the
// installer, so there is one copy of it and every endpoint gets it — including
// the ones installed before it existed, which the installer will never revisit.
//
//go:embed uninstall_windows.ps1
var uninstallScript string

// Windows lists installed software from the Uninstall registry keys and from
// nothing else, so registering a service leaves the agent invisible: absent from
// Programs and Features, from Settings ▸ Apps, and from every inventory tool
// that reads them. Software nobody can see on a managed endpoint is a support
// and audit problem.
//
// The installer writes this entry too. The agent repeats it because **agents
// update themselves and installers do not re-run**: an endpoint installed before
// the entry existed would never get one, and its DisplayVersion would freeze at
// whatever version first wrote it. Rewriting on every start is what keeps the
// two honest, and it is cheap — four values and a compare.
const (
	arpKeyPath      = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\OpenRMMAgent`
	arpDisplayName  = "OpenRMM Agent"
	arpPublisher    = "OpenRMM"
	arpUninstallPS1 = "uninstall.ps1"
)

// registerUninstallEntry is best effort by design. Failing to advertise the
// agent must never stop it from running: an endpoint that is managed but
// unlisted is far better than one that refuses to start because a registry
// write was denied.
func registerUninstallEntry() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	script := filepath.Join(dir, arpUninstallPS1)
	// Rewritten when it differs: an endpoint that updates gets the current
	// uninstaller, and one that never had it gets it now. An entry whose
	// UninstallString cannot run is worse than no entry at all — the operator
	// clicks Uninstall and nothing happens.
	if current, err := os.ReadFile(script); err != nil || string(current) != uninstallScript {
		if err := os.WriteFile(script, []byte(uninstallScript), 0o644); err != nil {
			log.Printf("windows: could not write %s: %v", script, err)
			return
		}
	}

	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, arpKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		log.Printf("windows: could not register the uninstall entry: %v", err)
		return
	}
	defer key.Close()

	command := fmt.Sprintf(`"%s\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "%s"`,
		strings.TrimRight(os.Getenv("SystemRoot"), `\`), script)
	values := []struct {
		name  string
		value string
	}{
		{"DisplayName", arpDisplayName},
		{"DisplayVersion", agentVersion},
		{"Publisher", arpPublisher},
		{"InstallLocation", dir},
		{"DisplayIcon", exe + ",0"},
		{"UninstallString", command},
		{"QuietUninstallString", command},
	}
	for _, v := range values {
		if current, _, err := key.GetStringValue(v.name); err == nil && current == v.value {
			continue // unchanged: do not rewrite the registry on every start
		}
		if err := key.SetStringValue(v.name, v.value); err != nil {
			log.Printf("windows: uninstall entry %s: %v", v.name, err)
			return
		}
	}
	for _, name := range []string{"NoModify", "NoRepair"} {
		if err := key.SetDWordValue(name, 1); err != nil {
			log.Printf("windows: uninstall entry %s: %v", name, err)
			return
		}
	}
	if info, err := os.Stat(exe); err == nil {
		_ = key.SetDWordValue("EstimatedSize", uint32(info.Size()/1024))
	}
}
