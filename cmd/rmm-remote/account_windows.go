package main

import (
	"syscall"
	"unsafe"
)

var (
	wtsapi32                        = syscall.NewLazyDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory               = wtsapi32.NewProc("WTSFreeMemory")
)

const (
	wtsCurrentSession = 0xFFFFFFFF // WTS_CURRENT_SESSION: the session this process runs in
	wtsUserName       = 5          // WTS_INFO_CLASS WTSUserName
)

// sessionAccount is the person signed in to the session the sidecar runs in.
// The agent starts it as SYSTEM placed into that session (SYSTEM is what can
// reach the UAC prompt), so the process's own account says nothing about who
// is at the machine; the session does. Empty at the sign-in screen.
func sessionAccount() string {
	var buf *uint16
	var size uint32
	r, _, _ := procWTSQuerySessionInformationW.Call(0, wtsCurrentSession, wtsUserName,
		uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&size)))
	if r == 0 || buf == nil {
		return processAccount()
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
	if a := accountName(syscall.UTF16ToString(unsafe.Slice(buf, size/2))); a != "" {
		return a
	}
	return processAccount()
}
