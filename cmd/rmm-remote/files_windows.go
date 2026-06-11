//go:build windows

package main

import (
	"os"

	"github.com/gorilla/websocket"
)

// runFilesOnly serves a Windows file-transfer session with NO screen capture.
// It reuses the media session's file-op handlers (onMessage routes fls/fget/
// fput/… to them) but never starts the desktopWorker, UAC handling, or chat —
// so "Remote ▸ File transfer" opens only the file browser, not a remote desktop.
// Input/chat/kf frames that arrive are harmlessly ignored (no worker drains
// them). Mirrors the POSIX runFilesOnly in files.go.
func runFilesOnly(conn *websocket.Conn) error {
	s := &session{conn: conn, out: make(chan wsMsg, 32), done: make(chan struct{}),
		inputCh: make(chan []byte, 256), uploads: map[uint8]*os.File{}}
	defer close(s.done)

	errc := make(chan error, 2)
	go func() {
		for {
			select {
			case <-s.done:
				return
			case m := <-s.out:
				if err := conn.WriteMessage(m.mt, m.data); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	s.sendJSON(map[string]any{"t": "hello", "v": remoteVersion, "mode": "files"})

	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			s.onMessage(mt, data)
		}
	}()
	return <-errc
}
