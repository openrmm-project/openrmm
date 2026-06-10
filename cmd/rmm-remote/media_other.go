//go:build !windows && !linux && !darwin

package main

import (
	"fmt"
	"runtime"

	"github.com/gorilla/websocket"
)

// runMedia is a stub on platforms without a native capturer. Windows lives in
// media_windows.go, Linux (X11) in media_linux.go, and macOS (CoreGraphics) in
// media_darwin.go. Everything else (e.g. *BSD) lands here. We return an error
// so the relay session ends cleanly instead of hanging with a black screen.
func runMedia(_ *websocket.Conn) error {
	return fmt.Errorf("native remote media engine not yet implemented on %s", runtime.GOOS)
}
