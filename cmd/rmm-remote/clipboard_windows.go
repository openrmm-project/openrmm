package main

import (
	"syscall"
	"unsafe"
)

// The technician's clipboard, pushed into the endpoint's. Windows hands clipboard
// ownership to whoever opened and emptied it, so the sequence is fixed: open,
// empty, write, close — and the memory handed to SetClipboardData belongs to the
// system afterwards, which is why it is never freed here on success.
var (
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procCloseClipboard   = user32.NewProc("CloseClipboard")

	// kernel32 itself is declared with the chat window's helpers.
	procGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	procGlobalLock   = kernel32.NewProc("GlobalLock")
	procGlobalUnlock = kernel32.NewProc("GlobalUnlock")
	procGlobalFree   = kernel32.NewProc("GlobalFree")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

func setClipboard(text string) error {
	encoded, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	size := uintptr(len(encoded) * 2)
	handle, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if handle == 0 {
		return err
	}
	locked, _, err := procGlobalLock.Call(handle)
	if locked == 0 {
		_, _, _ = procGlobalFree.Call(handle)
		return err
	}
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(locked)), len(encoded))
	copy(dst, encoded)
	_, _, _ = procGlobalUnlock.Call(handle)

	if ok, _, err := procOpenClipboard.Call(0); ok == 0 {
		_, _, _ = procGlobalFree.Call(handle)
		return err
	}
	defer procCloseClipboard.Call()
	if ok, _, err := procEmptyClipboard.Call(); ok == 0 {
		_, _, _ = procGlobalFree.Call(handle)
		return err
	}
	if res, _, err := procSetClipboardData.Call(cfUnicodeText, handle); res == 0 {
		_, _, _ = procGlobalFree.Call(handle)
		return err
	}
	return nil
}

// pasteShortcut sends Ctrl+V, so the technician's own paste key reaches the
// endpoint as the shortcut this platform uses rather than as a raw keystroke.
func pasteShortcut() {
	const vkControl, vkV = 0x11, 0x56
	sendKey(vkControl, true)
	sendKey(vkV, true)
	sendKey(vkV, false)
	sendKey(vkControl, false)
}
