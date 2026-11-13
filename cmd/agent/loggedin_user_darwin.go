//go:build darwin

package main

import "os/user"

// loggedInUser reports the user who owns the console: the person logged in at
// the Mac. At the login window the console belongs to root and no one is.
func loggedInUser() string {
	uid, err := consoleUID()
	if err != nil {
		return ""
	}
	u, err := user.LookupId(uid)
	if err != nil {
		return ""
	}
	return u.Username
}
