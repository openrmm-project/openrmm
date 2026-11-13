package main

import (
	"strings"

	"github.com/shirou/gopsutil/v3/host"
)

// The interactive user (Devices ▸ Logged-in user). Each heartbeat carries the
// user of the device's active interactive session, so operators can find a
// user's machine, triage support and check device assignments. "" means no
// one is logged in. The platform files read the session; these functions
// choose among sessions and format the name, and are the same everywhere.

// winSession is one session WTSEnumerateSessions reported as active.
type winSession struct {
	ID           uint32
	Domain, User string
}

// windowsSessionUser picks the user at the physical console when someone is
// logged in there, else the first other active session (RDP), and names it
// DOMAIN\user — AzureAD\user for an Entra ID account, the machine name for a
// local one.
func windowsSessionUser(sessions []winSession, consoleID uint32) string {
	var first string
	for _, s := range sessions {
		name := qualifiedUser(s.Domain, s.User)
		if name == "" {
			continue
		}
		if s.ID == consoleID {
			return name
		}
		if first == "" {
			first = name
		}
	}
	return first
}

func qualifiedUser(domain, user string) string {
	user, domain = strings.TrimSpace(user), strings.TrimSpace(domain)
	if user == "" {
		return ""
	}
	if domain == "" {
		return user
	}
	return domain + `\` + user
}

// utmpSessionUser picks, from the logged-in users utmp lists, someone at the
// machine itself (a local terminal or graphical seat: no host, or an X display
// such as ":0") before someone connected over the network (SSH).
func utmpSessionUser(users []host.UserStat) string {
	var remote string
	for _, u := range users {
		name := strings.TrimSpace(u.User)
		if name == "" || name == "LOGIN" || name == "reboot" {
			continue
		}
		if u.Host == "" || strings.HasPrefix(u.Host, ":") {
			return name
		}
		if remote == "" {
			remote = name
		}
	}
	return remote
}
