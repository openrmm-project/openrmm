package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/p1ngu/rmm/internal/models"
)

// High-level tools are semantic, OS-aware actions. The AI supplies the INTENT
// (install this, restart that); the backend builds the correct, vetted script
// for the target device's OS. Like propose_script, they create an
// approval-gated job — the AI never executes directly.
//
// These are code-defined (no per-deployment maintenance). The generic
// propose_script remains as the fallback for anything without a dedicated tool.

// highLevelTools is appended to the catalog advertised to the model / MCP.
func highLevelTools() []Tool {
	dev := `"agent_id":{"type":"string","description":"target device id"}`
	return []Tool{
		{Type: "function", Function: FunctionDef{
			Name:        "install_software",
			Description: "Propose installing a software package on a device. Builds the correct silent-install command for the device OS (winget on Windows, apt on Debian/Ubuntu, Homebrew on macOS). Pass the package identifier appropriate for that OS (e.g. '7zip.7zip' for winget, 'p7zip-full' for apt).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + dev + `,"package":{"type":"string","description":"OS-appropriate package id/name"}},"required":["agent_id","package"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "uninstall_software",
			Description: "Propose uninstalling a software package on a device (winget/apt/brew per OS).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + dev + `,"package":{"type":"string"}},"required":["agent_id","package"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "restart_service",
			Description: "Propose restarting a system service (Restart-Service on Windows, systemctl on Linux).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + dev + `,"service":{"type":"string","description":"service name"}},"required":["agent_id","service"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "install_printer",
			Description: "Propose installing a network printer by IP (Add-Printer over a TCP/IP port on Windows, lpadmin on Linux/macOS).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + dev + `,"name":{"type":"string","description":"printer display name"},"ip":{"type":"string","description":"printer IP address"},"driver":{"type":"string","description":"driver/model name (Windows); optional"}},"required":["agent_id","name","ip"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "flush_dns",
			Description: "Propose flushing the DNS resolver cache (ipconfig /flushdns on Windows, resolvectl on Linux, dscacheutil on macOS).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + dev + `},"required":["agent_id"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "reboot_device",
			Description: "Propose rebooting a device after an optional delay (seconds). Destructive — requires human approval like everything else.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + dev + `,"delay_seconds":{"type":"integer","description":"delay before reboot, default 60"}},"required":["agent_id"],"additionalProperties":false}`),
		}},
	}
}

// highLevelNames is the set handled by executeHighLevel.
var highLevelNames = map[string]bool{
	"install_software": true, "uninstall_software": true, "restart_service": true,
	"install_printer": true, "flush_dns": true, "reboot_device": true,
}

// executeHighLevel handles a high-level tool; ok=false means "not mine".
func executeHighLevel(ctx context.Context, tc ToolContext, name string, args json.RawMessage) (ToolResult, bool) {
	if !highLevelNames[name] {
		return ToolResult{}, false
	}
	var in struct {
		AgentID      string `json:"agent_id"`
		Package      string `json:"package"`
		Service      string `json:"service"`
		Name         string `json:"name"`
		IP           string `json:"ip"`
		Driver       string `json:"driver"`
		DelaySeconds int    `json:"delay_seconds"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errResult(err.Error()), true
	}
	agent, err := tc.Store.GetAgent(ctx, in.AgentID)
	if err != nil {
		return errResult("agent: " + err.Error()), true
	}

	var shell, script, desc string
	switch name {
	case "install_software":
		if e := safeToken(in.Package, false); e != "" {
			return errResult(e), true
		}
		shell, script, err = pkgCmd(agent.OS, in.Package, true)
		desc = "Install software: " + in.Package
	case "uninstall_software":
		if e := safeToken(in.Package, false); e != "" {
			return errResult(e), true
		}
		shell, script, err = pkgCmd(agent.OS, in.Package, false)
		desc = "Uninstall software: " + in.Package
	case "restart_service":
		if e := safeToken(in.Service, false); e != "" {
			return errResult(e), true
		}
		shell, script, err = serviceRestartCmd(agent.OS, in.Service)
		desc = "Restart service: " + in.Service
	case "install_printer":
		if e := safeToken(in.Name, true); e != "" {
			return errResult("name: " + e), true
		}
		if net.ParseIP(in.IP) == nil {
			return errResult("ip: not a valid IP address"), true
		}
		if in.Driver != "" {
			if e := safeToken(in.Driver, true); e != "" {
				return errResult("driver: " + e), true
			}
		}
		shell, script, err = installPrinterCmd(agent.OS, in.Name, in.IP, in.Driver)
		desc = fmt.Sprintf("Install printer %q at %s", in.Name, in.IP)
	case "flush_dns":
		shell, script, err = flushDNSCmd(agent.OS)
		desc = "Flush DNS cache"
	case "reboot_device":
		shell, script, err = rebootCmd(agent.OS, in.DelaySeconds)
		desc = "Reboot device"
	}
	if err != nil {
		return errResult(err.Error()), true
	}
	return proposeJob(ctx, tc, in.AgentID, shell, script, desc), true
}

// ---- OS-aware command builders ----

func pkgCmd(os, pkg string, install bool) (string, string, error) {
	switch os {
	case "windows":
		if install {
			return models.ShellPowerShell, "winget install --id " + pkg + " --silent --accept-package-agreements --accept-source-agreements", nil
		}
		return models.ShellPowerShell, "winget uninstall --id " + pkg + " --silent", nil
	case "linux":
		if install {
			return models.ShellBash, "sudo apt-get update -qq && sudo apt-get install -y " + pkg, nil
		}
		return models.ShellBash, "sudo apt-get remove -y " + pkg, nil
	case "darwin":
		if install {
			return models.ShellBash, "brew install " + pkg, nil
		}
		return models.ShellBash, "brew uninstall " + pkg, nil
	}
	return "", "", fmt.Errorf("unsupported OS %q for package management", os)
}

func serviceRestartCmd(os, svc string) (string, string, error) {
	switch os {
	case "windows":
		return models.ShellPowerShell, "Restart-Service -Name '" + svc + "' -Force; Get-Service '" + svc + "' | Select-Object Status,Name", nil
	case "linux":
		return models.ShellBash, "sudo systemctl restart " + svc + " && systemctl is-active " + svc, nil
	case "darwin":
		return models.ShellBash, "sudo launchctl kickstart -k system/" + svc, nil
	}
	return "", "", fmt.Errorf("unsupported OS %q for service control", os)
}

func installPrinterCmd(os, name, ip, driver string) (string, string, error) {
	switch os {
	case "windows":
		port := "IP_" + ip
		drv := driver
		if drv == "" {
			drv = "Microsoft IPP Class Driver"
		}
		script := strings.Join([]string{
			"Add-PrinterPort -Name '" + port + "' -PrinterHostAddress '" + ip + "' -ErrorAction SilentlyContinue",
			"Add-Printer -Name '" + name + "' -DriverName '" + drv + "' -PortName '" + port + "'",
			"Get-Printer -Name '" + name + "'",
		}, "\n")
		return models.ShellPowerShell, script, nil
	case "linux", "darwin":
		// CUPS via lpadmin using the IPP everywhere driver.
		return models.ShellBash, "sudo lpadmin -p '" + sanitizeName(name) + "' -E -v 'ipp://" + ip + "/ipp/print' -m everywhere && lpstat -p", nil
	}
	return "", "", fmt.Errorf("unsupported OS %q for printer install", os)
}

func flushDNSCmd(os string) (string, string, error) {
	switch os {
	case "windows":
		return models.ShellCmd, "ipconfig /flushdns", nil
	case "linux":
		return models.ShellBash, "sudo resolvectl flush-caches 2>/dev/null || sudo systemd-resolve --flush-caches 2>/dev/null || echo 'no systemd-resolved'", nil
	case "darwin":
		return models.ShellBash, "sudo dscacheutil -flushcache && sudo killall -HUP mDNSResponder", nil
	}
	return "", "", fmt.Errorf("unsupported OS %q for DNS flush", os)
}

func rebootCmd(os string, delay int) (string, string, error) {
	if delay <= 0 {
		delay = 60
	}
	switch os {
	case "windows":
		return models.ShellCmd, fmt.Sprintf("shutdown /r /t %d /c \"Reboot scheduled via RMM\"", delay), nil
	case "linux", "darwin":
		return models.ShellBash, fmt.Sprintf("sudo shutdown -r +%d 'Reboot scheduled via RMM'", (delay+59)/60), nil
	}
	return "", "", fmt.Errorf("unsupported OS %q for reboot", os)
}

// ---- helpers ----

// safeToken rejects shell metacharacters to prevent command injection in the
// generated script (defense in depth — approval is still required). Returns ""
// when safe, or an error message.
func safeToken(s string, allowSpace bool) string {
	if strings.TrimSpace(s) == "" {
		return "value is required"
	}
	bad := ";|&$`<>(){}[]!\\\n\r\t\"'*?~"
	for _, c := range s {
		if strings.ContainsRune(bad, c) {
			return "value contains a disallowed character"
		}
		if c == ' ' && !allowSpace {
			return "value may not contain spaces"
		}
	}
	return ""
}

func sanitizeName(s string) string { return strings.ReplaceAll(s, " ", "_") }

func errResult(msg string) ToolResult {
	return ToolResult{Output: fmt.Sprintf(`{"error":%q}`, msg)}
}
