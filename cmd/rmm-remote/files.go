//go:build !windows

package main

import (
	"encoding/json"
	"log"

	"github.com/gorilla/websocket"
)

// runFilesOnly serves a file-transfer session WITHOUT capturing or streaming the
// screen — the agent module behind "Remote ▸ File transfer". On POSIX it reuses
// the standalone file-transfer implementation (newFileXfer, transfer_posix.go)
// and the same relay wire protocol as runMedia (hello + JSON control +
// [chFile][id] chunks), minus the capture/input/video paths. (Windows has its
// own runFilesOnly in files_windows.go, reusing its session-bound file ops.)
// Opening file transfer therefore never starts a remote-desktop session; only an
// explicit desktop session does (and it can still open the file browser in-band).
func runFilesOnly(conn *websocket.Conn) error {
	type omsg struct {
		mt int
		b  []byte
	}
	out := make(chan omsg, 32)
	done := make(chan struct{})
	defer close(done)
	errc := make(chan error, 2)

	// single writer (gorilla forbids concurrent writes)
	go func() {
		for {
			select {
			case <-done:
				return
			case m := <-out:
				if err := conn.WriteMessage(m.mt, m.b); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	xfer := newFileXfer(func(mt int, b []byte) {
		select {
		case out <- omsg{mt, b}:
		case <-done:
		}
	})

	if b, e := json.Marshal(map[string]any{"t": "hello", "v": remoteVersion, "mode": "files"}); e == nil {
		out <- omsg{websocket.TextMessage, b}
	}
	log.Printf("rmm-remote: file-transfer-only session (no screen capture)")

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if mt == websocket.BinaryMessage {
			// viewer -> agent upload chunk: [chFile][id][bytes]
			if len(data) >= 2 && data[0] == chFile {
				xfer.onChunk(data[1], data[2:])
			}
			continue
		}
		if mt != websocket.TextMessage {
			continue
		}
		var e struct {
			T string `json:"t"`
		}
		if json.Unmarshal(data, &e) != nil {
			continue
		}
		// Ignore input/video/chat control here; only file ops are served.
		xfer.dispatch(e.T, data)
	}
}
