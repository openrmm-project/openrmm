package main

import (
	"os/user"
	"strings"
)

// Chat lines from the console side are queued for the chat window with a
// prefix that says whose they are, and each window labels and colours a line
// by it: "Tech: " is the technician's, "You: " the person's own reply, and
// chatGuestPrefix a guest's.
const chatGuestPrefix = "Guest: "

// chatLine is a line from the console side as the chat window queues it. A
// guest's line — carried over by the server from the guest's own session
// (internal/api/api_guest_chat.go) — is shown under the guest's name and kept
// on one line, so it cannot start a line that seems to be someone else's.
func chatLine(from, name, text string) string {
	if from != "guest" {
		return "Tech: " + text
	}
	return chatGuestPrefix + guestChatText(name, text)
}

// guestChatText is a guest's line as the window shows it: "<name> (guest): …".
func guestChatText(name, text string) string {
	label := oneLine(name)
	if label == "" {
		label = "?"
	}
	return label + " (guest): " + oneLine(text)
}

// accountName is a login as the chat shows it: without a Windows domain or
// computer prefix, and empty for a service account, which is never the person
// at the machine.
func accountName(login string) string {
	if i := strings.LastIndexByte(login, '\\'); i >= 0 {
		login = login[i+1:]
	}
	switch strings.ToLower(login) {
	case "system", "local service", "network service", "root":
		return ""
	}
	return oneLine(login)
}

// processAccount is the account the sidecar runs as, when it is a person's. On
// macOS the agent starts it as the signed-in user. On Windows it runs as SYSTEM
// in the user's session and on Linux as root, so sessionAccount asks the
// session (account_windows.go) or the desktop (seat_linux.go) instead.
func processAccount() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return accountName(u.Username)
}

// selfLabel is how a chat window names the person at the machine: by their
// account when it is known, else "You". The console names them the same way,
// from the "user" the hello announces.
func selfLabel(account string) string {
	if account == "" {
		return "You"
	}
	return account
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s))
}
