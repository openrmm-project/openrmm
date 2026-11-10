package models

import "strings"

// These checks require the native systemd/launchd runners, added in 0.10.0.
const MinAgentVersionServiceChecks = "0.10.0"

func IsPlatformServiceCheck(kind string) bool {
	return kind == CheckLinuxService || kind == CheckMacService
}

// Policy checks on mixed fleets apply only to their service manager's OS.
func ServiceCheckMatchesOS(kind, goos string) bool {
	switch kind {
	case CheckLinuxService:
		return strings.EqualFold(goos, "linux")
	case CheckMacService:
		return strings.EqualFold(goos, "darwin") || strings.EqualFold(goos, "macos")
	default:
		return true
	}
}
