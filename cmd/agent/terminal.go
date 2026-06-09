package main

import (
	"encoding/json"
	"log"
	"os/user"
	"runtime"

	"github.com/gorilla/websocket"

	"github.com/p1ngu/rmm/internal/protocol"
)

// termSession is an OS-specific interactive shell the agent bridges to the relay.
// The Unix implementation (pty_unix.go) runs it on a real PTY; Windows
// (pty_windows.go) uses pipes. Both inherit the agent's privileges.
type termSession interface {
	Read(p []byte) (int, error)  // shell output -> viewer
	Write(p []byte) (int, error) // viewer keystrokes -> shell
	Resize(cols, rows uint16) error
	Name() string // the launched shell (bash, sh, powershell, ...)
	Close() error
}

// startTerminalSession opens an elevated interactive shell on the endpoint and
// bridges it to the relay viewer. Unlike the media sidecar (a separate process
// in the interactive desktop), the shell runs IN the agent process, so it
// inherits the agent's privileges — and the agent runs as a system service
// (root on Linux/macOS, SYSTEM on Windows), so the session is elevated.
//
// Relay wire format (rides the existing relay, which just pipes frames):
//
//	viewer -> agent : binary = raw stdin bytes (keystrokes)
//	                  text   = JSON control, {"t":"resize","cols":C,"rows":R}
//	agent  -> viewer: binary = raw stdout/stderr bytes
//	                  text   = JSON {"t":"hello",...} on connect, {"t":"exit"} at end
func (a *agent) startTerminalSession(rs protocol.RemoteStartMsg) {
	relay := a.relayURL(rs.Token)
	log.Printf("terminal: opening elevated shell for session %s", rs.SessionID)

	conn, _, err := websocket.DefaultDialer.Dial(relay, nil)
	if err != nil {
		log.Printf("terminal: dial relay: %v", err)
		return
	}
	defer conn.Close()

	term, err := startShell()
	if err != nil {
		log.Printf("terminal: start shell: %v", err)
		writeTermText(conn, map[string]any{"t": "error", "msg": "could not start shell: " + err.Error()})
		return
	}
	defer term.Close()

	uname := ""
	if u, e := user.Current(); e == nil {
		uname = u.Username
	}
	writeTermText(conn, map[string]any{
		"t": "hello", "shell": term.Name(), "user": uname,
		"elevated": elevated(), "os": runtime.GOOS,
	})

	// Shell output -> viewer (binary). The relay preserves the message type, so
	// the browser receives an arraybuffer it writes straight into xterm.js.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := term.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
		writeTermText(conn, map[string]any{"t": "exit"})
		conn.Close() // unblock the read loop below
	}()

	// Viewer -> shell. Binary frames are keystrokes; text frames are control.
	for {
		mt, data, rerr := conn.ReadMessage()
		if rerr != nil {
			break
		}
		switch mt {
		case websocket.BinaryMessage:
			_, _ = term.Write(data)
		case websocket.TextMessage:
			var ctrl struct {
				T    string `json:"t"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &ctrl) == nil && ctrl.T == "resize" {
				_ = term.Resize(ctrl.Cols, ctrl.Rows)
			}
		}
	}
	log.Printf("terminal: session %s ended", rs.SessionID)
}

// writeTermText sends a JSON control frame to the viewer. Only the agent's main
// goroutine calls this before the output pump starts, plus the pump itself at
// exit — never concurrently — so it is safe on a single websocket writer.
func writeTermText(conn *websocket.Conn, v any) {
	data, _ := json.Marshal(v)
	_ = conn.WriteMessage(websocket.TextMessage, data)
}
