//go:build !windows

package main

import (
	"fmt"
	"runtime"

	"github.com/gorilla/websocket"
)

// runMedia is a stub on non-Windows platforms. The Windows reference engine
// lives in media_windows.go; Linux/macOS capture+input is a follow-up (and the
// RustDesk fork covers all three). We return an error so the relay session ends
// cleanly instead of hanging with a black screen.
func runMedia(_ *websocket.Conn) error {
	return fmt.Errorf("native remote media engine not yet implemented on %s", runtime.GOOS)
}
