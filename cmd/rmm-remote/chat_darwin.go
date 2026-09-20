//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
void rmmUIRun(void);
void rmmUIStop(void);
void rmmChatOpen(void);
void rmmChatAppend(const char *text, int technician);
void rmmChatClose(void);
int rmmSetClipboard(const char *text);
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

func openMacChat(send func(string)) {
	macChat.Lock()
	macChat.send = send
	macChat.Unlock()
	C.rmmChatOpen()
}

func appendMacChat(text string, technician bool) {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	role := C.int(0)
	if technician {
		role = 1
	}
	C.rmmChatAppend(c, role)
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
