package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

// ---------- scan ----------

// scanPatches returns the OS patches currently missing on this host. Each OS
// scanner shells out to the native tool (WUA / apt / dnf / softwareupdate) — all
// CGO-free. NOTE: validate the exact parsing on real endpoints per OS.
func scanPatches() (protocol.PatchReportMsg, error) {
	rep := protocol.PatchReportMsg{OS: runtime.GOOS}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	switch runtime.GOOS {
	case "windows":
		var err error
		rep.Patches, err = scanWindowsPatches(ctx)
		return rep, err
	case "linux":
		var err error
		rep.Patches, err = scanLinuxPatches(ctx)
		return rep, err
	case "darwin":
		var err error
		rep.Patches, err = scanDarwinPatches(ctx)
		return rep, err
	default:
		return rep, fmt.Errorf("patch scanning is unsupported on %s", runtime.GOOS)
	}
}

const wuaScanPS = `$ErrorActionPreference='Stop'
$s = New-Object -ComObject Microsoft.Update.Session
$r = $s.CreateUpdateSearcher().Search("IsInstalled=0 and IsHidden=0")
$out = @()
foreach ($u in $r.Updates) {
  $kbs = @(); foreach ($k in $u.KBArticleIDs) { $kbs += "KB$k" }
  $cats = @(); foreach ($c in $u.Categories) { $cats += $c.Name }
  $id = if ($kbs.Count) { $kbs[0] } else { $u.Identity.UpdateID }
  $out += [pscustomobject]@{
    identifier = $id
    title      = $u.Title
    severity   = $u.MsrcSeverity
    categories = ($cats -join ',')
    kb         = ($kbs -join ',')
    reboot     = ($u.InstallationBehavior.RebootBehavior -eq 1)
  }
}
$out | ConvertTo-Json -Compress`

func scanWindowsPatches(ctx context.Context) ([]protocol.PatchItem, error) {
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", wuaScanPS).Output()
	if err != nil {
		return nil, fmt.Errorf("windows update scan: %w", err)
	}
	return parseWindowsPatchOutput(out)
}

// parseWindowsPatchOutput decodes PowerShell's redirected output before JSON
// unmarshalling. Windows PowerShell writes in the active Windows code page, so
// handing those bytes directly to encoding/json replaces accented characters
// with U+FFFD and permanently corrupts titles such as "Atualização de Segurança".
func parseWindowsPatchOutput(out []byte) ([]protocol.PatchItem, error) {
	trimmed := strings.TrimSpace(decodeOutput(out))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		trimmed = "[" + trimmed + "]"
	}
	var arr []struct {
		Identifier, Title, Severity, Categories, KB string
		Reboot                                      bool
	}
	if json.Unmarshal([]byte(trimmed), &arr) != nil {
		return nil, fmt.Errorf("windows update scan returned invalid JSON")
	}
	var items []protocol.PatchItem
	for _, u := range arr {
		if u.Identifier == "" {
			continue
		}
		items = append(items, protocol.PatchItem{
			Identifier:     u.Identifier,
			Title:          u.Title,
			Classification: classifyWindows(u.Severity, u.Categories),
			KB:             u.KB,
			RebootRequired: u.Reboot,
		})
	}
	return items, nil
}

func classifyWindows(severity, categories string) string {
	if strings.Contains(strings.ToLower(categories), "security") {
		return protocol.PatchSecurity
	}
	switch strings.ToLower(severity) {
	case "critical":
		return protocol.PatchCritical
	case "important":
		return protocol.PatchImportant
	case "moderate":
		return protocol.PatchModerate
	case "low":
		return protocol.PatchOptional
	default:
		return protocol.PatchOther
	}
}

func scanLinuxPatches(ctx context.Context) ([]protocol.PatchItem, error) {
	if hasCmd("apt-get") {
		return scanAptPatches(ctx)
	}
	if hasCmd("dnf") {
		return scanDnfPatches(ctx)
	}
	return nil, fmt.Errorf("no supported Linux package manager found")
}

func hasCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// scanAptPatches parses `apt-get -s upgrade`'s "Inst" lines. Security is inferred
// from the package origin suite (…-security).
func scanAptPatches(ctx context.Context) ([]protocol.PatchItem, error) {
	out, err := exec.CommandContext(ctx, "apt-get", "-s", "-o", "Debug::NoLocking=true", "upgrade").Output()
	if err != nil {
		return nil, fmt.Errorf("apt patch scan: %w", err)
	}
	var items []protocol.PatchItem
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		// Inst <pkg> [old] (<newver> <origin...> [arch])
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pkg := fields[1]
		ver, origin := "", line
		if i := strings.Index(line, "("); i >= 0 {
			inner := strings.Trim(line[i+1:], ") ")
			fs := strings.Fields(inner)
			if len(fs) > 0 {
				ver = fs[0]
			}
			origin = inner
		}
		class := protocol.PatchOther
		if strings.Contains(strings.ToLower(origin), "security") {
			class = protocol.PatchSecurity
		}
		items = append(items, protocol.PatchItem{
			Identifier: pkg, Title: pkg + " " + ver, Classification: class, Version: ver,
		})
	}
	return items, nil
}

// scanDnfPatches parses `dnf -q updateinfo list` (advisory type/severity pkg).
func scanDnfPatches(ctx context.Context) ([]protocol.PatchItem, error) {
	out, err := exec.CommandContext(ctx, "dnf", "-q", "updateinfo", "list", "--available").Output()
	if err != nil {
		return nil, fmt.Errorf("dnf patch scan: %w", err)
	}
	var items []protocol.PatchItem
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		advisory, typ, pkg := fields[0], fields[1], fields[len(fields)-1]
		items = append(items, protocol.PatchItem{
			Identifier:     pkg,
			Title:          pkg + " (" + advisory + ")",
			Classification: classifyDnf(typ),
		})
	}
	return items, nil
}

func classifyDnf(typ string) string {
	t := strings.ToLower(typ)
	switch {
	case strings.Contains(t, "critical"):
		return protocol.PatchCritical
	case strings.Contains(t, "important"):
		return protocol.PatchImportant
	case strings.Contains(t, "moderate"):
		return protocol.PatchModerate
	case strings.Contains(t, "sec"):
		return protocol.PatchSecurity
	case strings.Contains(t, "bugfix"):
		return protocol.PatchOptional
	default:
		return protocol.PatchOther
	}
}

func scanDarwinPatches(ctx context.Context) ([]protocol.PatchItem, error) {
	out, err := exec.CommandContext(ctx, "softwareupdate", "-l").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("macOS softwareupdate scan: %w", err)
	}
	var items []protocol.PatchItem
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, "* Label:") {
			continue
		}
		label := strings.TrimSpace(strings.TrimPrefix(l, "* Label:"))
		title, reboot := label, false
		if i+1 < len(lines) {
			det := lines[i+1]
			title = strings.TrimSpace(det)
			reboot = strings.Contains(strings.ToLower(det), "restart")
		}
		class := protocol.PatchOther
		if strings.Contains(strings.ToLower(title), "security") {
			class = protocol.PatchSecurity
		}
		items = append(items, protocol.PatchItem{
			Identifier: label, Title: title, Classification: class, RebootRequired: reboot,
		})
	}
	return items, nil
}

// ---------- install ----------

// installPatches installs explicit server-approved patch identifiers and returns
// per-patch results plus whether a reboot is now required. Empty identifiers are
// rejected so no OS package manager can interpret them as "install everything".
func installPatches(identifiers []string) ([]protocol.PatchResultItem, bool) {
	if len(identifiers) == 0 {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	switch runtime.GOOS {
	case "windows":
		return installWindowsPatches(ctx, identifiers)
	case "linux":
		return installLinuxPatches(ctx, identifiers)
	case "darwin":
		return installDarwinPatches(ctx, identifiers)
	}
	return nil, false
}

// uniformResult marks every requested identifier success/failure (used by the
// package managers, which report a single overall exit code).
func uniformResult(ids []string, ok bool, reboot bool, errMsg string) ([]protocol.PatchResultItem, bool) {
	var res []protocol.PatchResultItem
	for _, id := range ids {
		item := protocol.PatchResultItem{Identifier: id, Success: ok, RebootRequired: reboot}
		if !ok {
			item.Error = errMsg
		}
		res = append(res, item)
	}
	return res, ok && reboot
}

const wuaInstallPS = `$ErrorActionPreference='Stop'
$want = '__IDS__'.Split(',') | Where-Object { $_ }
$s = New-Object -ComObject Microsoft.Update.Session
$r = $s.CreateUpdateSearcher().Search("IsInstalled=0 and IsHidden=0")
$coll = New-Object -ComObject Microsoft.Update.UpdateColl
$map = @{}
foreach ($u in $r.Updates) {
  $id = if ($u.KBArticleIDs.Count) { "KB" + $u.KBArticleIDs.Item(0) } else { $u.Identity.UpdateID }
  if ($want -contains $id) { [void]$coll.Add($u); $map[$u.Identity.UpdateID]=$id }
}
$results=@()
if ($coll.Count -gt 0) {
  $dl = $s.CreateUpdateDownloader(); $dl.Updates=$coll; [void]$dl.Download()
  $inst = $s.CreateUpdateInstaller(); $inst.Updates=$coll; $ir=$inst.Install()
  $reboot = $ir.RebootRequired
  for ($i=0; $i -lt $coll.Count; $i++) {
    $u=$coll.Item($i); $code=$ir.GetUpdateResult($i).ResultCode
    $results += [pscustomobject]@{ identifier=$map[$u.Identity.UpdateID]; success=($code -eq 2); reboot=$reboot }
  }
}
$results | ConvertTo-Json -Compress`

func installWindowsPatches(ctx context.Context, ids []string) ([]protocol.PatchResultItem, bool) {
	ps := strings.Replace(wuaInstallPS, "__IDS__", strings.Join(ids, ","), 1)
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return uniformResult(ids, false, false, "wua install failed: "+err.Error())
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "{") {
		trimmed = "[" + trimmed + "]"
	}
	var arr []struct {
		Identifier string
		Success    bool
		Reboot     bool
	}
	if json.Unmarshal([]byte(trimmed), &arr) != nil {
		return uniformResult(ids, false, false, "wua: bad result json")
	}
	var res []protocol.PatchResultItem
	reboot := false
	for _, r := range arr {
		if r.Reboot {
			reboot = true
		}
		item := protocol.PatchResultItem{Identifier: r.Identifier, Success: r.Success, RebootRequired: r.Reboot}
		if !r.Success {
			item.Error = "install failed"
		}
		res = append(res, item)
	}
	return res, reboot
}

func installLinuxPatches(ctx context.Context, ids []string) ([]protocol.PatchResultItem, bool) {
	if hasCmd("apt-get") {
		args := []string{"-y", "-o", "Dpkg::Options::=--force-confold"}
		if len(ids) == 0 {
			args = append(args, "upgrade")
		} else {
			args = append(args, "install", "--only-upgrade")
			args = append(args, ids...)
		}
		cmd := exec.CommandContext(ctx, "apt-get", args...)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		out, err := cmd.CombinedOutput()
		return uniformResult(ids, err == nil, fileExists("/var/run/reboot-required"), errOut(err, out))
	}
	if hasCmd("dnf") {
		args := append([]string{"-y", "upgrade"}, ids...)
		out, err := exec.CommandContext(ctx, "dnf", args...).CombinedOutput()
		reboot := exec.CommandContext(ctx, "dnf", "-q", "needs-restarting", "-r").Run() != nil
		return uniformResult(ids, err == nil, reboot, errOut(err, out))
	}
	return uniformResult(ids, false, false, "no supported package manager")
}

func installDarwinPatches(ctx context.Context, ids []string) ([]protocol.PatchResultItem, bool) {
	args := []string{"-i"}
	if len(ids) == 0 {
		args = append(args, "-a")
	} else {
		args = append(args, ids...)
	}
	out, err := exec.CommandContext(ctx, "softwareupdate", args...).CombinedOutput()
	return uniformResult(ids, err == nil, true, errOut(err, out))
}

func errOut(err error, out []byte) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(string(out))
	if len(msg) > 800 {
		msg = msg[len(msg)-800:]
	}
	if msg == "" {
		return err.Error()
	}
	return msg
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// handlePatchInstall runs an install request and reports the per-patch outcome.
func (a *agent) handlePatchInstall(m protocol.PatchInstallMsg) {
	a.patchMu.Lock()
	defer a.patchMu.Unlock()
	if len(m.Identifiers) == 0 {
		log.Printf("patch install %s rejected: explicit approved identifiers are required", m.RequestID)
		return
	}
	results, reboot := installPatches(m.Identifiers)
	a.queue(protocol.MsgPatchResult, protocol.PatchResultMsg{
		RequestID: m.RequestID, Results: results, RebootRequired: reboot,
	})
}

func (a *agent) runPatchScan() {
	a.patchMu.Lock()
	defer a.patchMu.Unlock()
	report, err := scanPatches()
	if err != nil {
		report.Error = err.Error()
	}
	a.queue(protocol.MsgPatchReport, report)
}

func (a *agent) handlePatchReboot(m protocol.RebootMsg) {
	a.patchMu.Lock()
	defer a.patchMu.Unlock()
	rebootHost(m.DelaySeconds)
}

// ---------- reboot ----------

func rebootHost(delaySeconds int) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("shutdown", "/r", "/t", strconv.Itoa(delaySeconds), "/c", "OpenRMM patch reboot").Start()
	default:
		mins := delaySeconds / 60
		when := "now"
		if mins > 0 {
			when = "+" + strconv.Itoa(mins)
		}
		_ = exec.Command("shutdown", "-r", when).Start()
	}
}
