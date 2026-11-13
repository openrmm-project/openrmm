//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")

// WTS_INFO_CLASS values used here.
const (
	wtsUserName   = 5
	wtsDomainName = 7
)

// loggedInUser reports the user of the active interactive session: the
// console if someone is logged in there, else an active remote desktop
// session. A disconnected session (a user switched away from, a closed remote
// desktop window) is not active.
func loggedInUser() string {
	var info *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &info, &count); err != nil || info == nil {
		return ""
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(info)))
	consoleID, _, _ := procWTSGetActiveSession.Call()
	var sessions []winSession
	for _, s := range unsafe.Slice(info, count) {
		if s.State != windows.WTSActive {
			continue
		}
		sessions = append(sessions, winSession{ID: s.SessionID, Domain: wtsSessionString(s.SessionID, wtsDomainName), User: wtsSessionString(s.SessionID, wtsUserName)})
	}
	return windowsSessionUser(sessions, uint32(consoleID))
}

func wtsSessionString(sessionID uint32, class uintptr) string {
	var buf *uint16
	var size uint32
	ok, _, _ := procWTSQuerySessionInformationW.Call(0, uintptr(sessionID), class,
		uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || buf == nil {
		return ""
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf)
}
