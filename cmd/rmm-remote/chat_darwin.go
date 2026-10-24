//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework Cocoa -framework ApplicationServices
#include <stdlib.h>
void rmmUIRun(void);
void rmmUIStop(void);
void rmmChatOpen(const char *selfName);
void rmmChatAppend(const char *text, int role);
void rmmChatClose(void);
int rmmSetClipboard(const char *text);
char *rmmReadClipboard(void);
void rmmPermissionNotice(int screenAllowed, int accessibilityAllowed);
*/
import "C"

import (
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

// AppKit's event loop must live on the initial process thread. File-only
// sessions never initialize Cocoa or open a window.
func init() { runtime.LockOSThread() }

func runPlatformUI(desktop bool, run func() error) error {
	if !desktop {
		return run()
	}
	result := make(chan error, 1)
	go func() { result <- run(); C.rmmUIStop() }()
	C.rmmUIRun()
	return <-result
}

var macChat struct {
	sync.RWMutex
	send func(string)
}

// openMacChat opens the window. account names the person here (selfLabel).
func openMacChat(send func(string), account string) {
	macChat.Lock()
	macChat.send = send
	macChat.Unlock()
	c := C.CString(account)
	defer C.free(unsafe.Pointer(c))
	C.rmmChatOpen(c)
}

// Who a line in the macOS chat window is from (rmmChatAppend's role).
const (
	chatRoleYou   = 0
	chatRoleTech  = 1
	chatRoleGuest = 2 // the text carries the guest's label (chatLine)
)

func appendMacChat(text string, role int) {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	C.rmmChatAppend(c, C.int(role))
}

func closeMacChat() {
	macChat.Lock()
	macChat.send = nil
	macChat.Unlock()
	C.rmmChatClose()
}

// The native TCC sheet is not guaranteed to reappear after a denial. Keep a
// host-side explanation with direct links as a reliable fallback.
func showMacPermissionNotice(screenAllowed, accessibilityAllowed bool) {
	screen := C.int(0)
	if screenAllowed {
		screen = 1
	}
	accessibility := C.int(0)
	if accessibilityAllowed {
		accessibility = 1
	}
	C.rmmPermissionNotice(screen, accessibility)
}

//export rmmChatSend
func rmmChatSend(value *C.char) {
	text := strings.TrimSpace(C.GoString(value))
	if text == "" {
		return
	}
	macChat.RLock()
	send := macChat.send
	macChat.RUnlock()
	if send != nil {
		go send(text)
	}
}

// setMacClipboard replaces this Mac's pasteboard with the technician's text.
func setMacClipboard(text string) bool {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	return C.rmmSetClipboard(c) != 0
}

// readClipboard returns this Mac's clipboard text, or an empty string when it
// holds something that is not text.
func readClipboard() (string, error) {
	c := C.rmmReadClipboard()
	if c == nil {
		return "", nil
	}
	defer C.free(unsafe.Pointer(c))
	return C.GoString(c), nil
}
