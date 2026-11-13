//go:build !windows && !darwin

package main

import "github.com/shirou/gopsutil/v3/host"

// loggedInUser reports a user logged in at the machine (utmp), preferring a
// local console or graphical seat over an SSH session.
func loggedInUser() string {
	users, err := host.Users()
	if err != nil {
		return ""
	}
	return utmpSessionUser(users)
}
