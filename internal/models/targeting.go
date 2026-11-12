package models

import (
	"slices"
	"strings"
)

// Policy targeting (docs/10, RFC #258). An assignment can narrow its scope to
// devices of one OS and/or one role; empty means any, which is how every
// assignment behaved before. A global assignment covers every device.
const (
	TargetOSWindows = "windows"
	TargetOSMacOS   = "macos"
	TargetOSLinux   = "linux"

	RoleWorkstation = "workstation"
	RoleServer      = "server"

	// ScopeGlobal assigns a policy to every device, through its filters.
	ScopeGlobal = "global"
	// GlobalScopeID is the scope id a global assignment carries.
	GlobalScopeID = "00000000-0000-0000-0000-000000000000"
)

// TargetOSes and DeviceRoles are the values a filter may take.
var (
	TargetOSes  = []string{TargetOSWindows, TargetOSMacOS, TargetOSLinux}
	DeviceRoles = []string{RoleWorkstation, RoleServer}
)

// ValidTargetFilter accepts an empty filter or a known value.
func ValidTargetFilter(os, role string) bool {
	return (os == "" || slices.Contains(TargetOSes, os)) && (role == "" || slices.Contains(DeviceRoles, role))
}

// TargetOS maps an agent's reported OS to the filter vocabulary.
func TargetOS(agentOS string) string {
	if agentOS == "darwin" {
		return TargetOSMacOS
	}
	return agentOS
}

// DetectRole derives a device's role from what its agent reports. Windows
// tells it exactly through its product type (gopsutil's PlatformFamily:
// "Standalone Workstation", "Server", "Server (Domain Controller)"); an agent
// that does not send it yet still names its edition ("Microsoft Windows Server
// 2022 Standard"). A Mac is a workstation. Linux cannot tell a server from a
// desktop reliably, so it has no role until an administrator sets one.
func DetectRole(agentOS, platform, platformFamily string) string {
	switch agentOS {
	case "darwin":
		return RoleWorkstation
	case "windows":
		family := strings.ToLower(strings.TrimSpace(platformFamily))
		switch {
		case strings.HasPrefix(family, "server"):
			return RoleServer
		case strings.Contains(family, "workstation"):
			return RoleWorkstation
		}
		name := strings.ToLower(platform)
		switch {
		case strings.Contains(name, "windows server"), strings.Contains(name, "server 20"):
			return RoleServer
		case strings.TrimSpace(name) != "":
			return RoleWorkstation
		}
	}
	return ""
}

// EffectiveRole is the role policies filter on: the override when set.
func EffectiveRole(detected, override string) string {
	if override != "" {
		return override
	}
	return detected
}

// PolicyExclusion is a device a policy or patch policy leaves out, whichever
// of its assignments reaches it.
type PolicyExclusion struct {
	AgentID   string `json:"agent_id"`
	Hostname  string `json:"hostname"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
}
