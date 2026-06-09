//go:build !windows && !linux

package main

import (
	"fmt"
	"runtime"

	"github.com/gorilla/websocket"
)

// runMedia is a stub on platforms without a native capturer. Windows lives in
// media_windows.go and Linux (X11) in media_linux.go; macOS has no CGO-free
// capture path yet. We return an error so the relay session ends cleanly
// instead of hanging with a black screen.
func runMedia(_ *websocket.Conn) error {
	return fmt.Errorf("native remote media engine not yet implemented on %s", runtime.GOOS)
}
