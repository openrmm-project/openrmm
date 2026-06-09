// Command rmm-remote is the native remote-desktop media sidecar.
//
// The RMM agent launches it on a remote_start: it dials the server relay
// (/ws/remote?role=agent), streams the screen as tile-delta video frames
// (binary WS messages; see encoder.go), and injects the input events it
// receives from the viewer (text WS messages, JSON). It is a SEPARATE process
// from the agent on purpose — capture/encode is per-OS while everything else
// (agent / relay / viewer) stays unchanged. Keeping it separate also lets it
// run in the interactive user session (the agent runs as a session-0 service,
// which cannot capture the user's desktop) and keeps the agent CGO-free.
//
// The video codec (encoder.go) is CGO-free and OS-agnostic: it diffs the
// captured RGBA frame in tiles and sends only what changed, so each platform
// only needs a native capturer (Windows GDI today, X11 next) — no fork.
//
// Relay wire format (both directions ride the existing relay, which just
// pipes frames between the two peers):
//   agent  -> viewer : binary message = a tile-delta video frame (encoder.go)
//   viewer -> agent  : text message   = a JSON input event, one of
//       {"t":"m","x":0.5,"y":0.5}                 mouse move (normalized 0..1)
//       {"t":"b","x":..,"y":..,"button":0,"down":true}  mouse button
//       {"t":"w","dy":120}                        wheel (browser deltaY)
//       {"t":"k","code":65,"down":true}           key (browser keyCode ~= VK)
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"
)

func main() {
	relay := flag.String("relay", os.Getenv("RMM_RELAY_URL"), "relay WebSocket URL (role=agent)")
	flag.Parse()
	// The sidecar is launched with CREATE_NO_WINDOW, so log to a file next to the
	// binary for diagnostics (why a session ends, capture errors, ...).
	if exe, err := os.Executable(); err == nil {
		if f, e := os.OpenFile(filepath.Join(filepath.Dir(exe), "rmm-remote.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); e == nil {
			log.SetOutput(f)
		}
	}
	if *relay == "" {
		log.Fatal("rmm-remote: -relay (or RMM_RELAY_URL) is required")
	}
	log.Printf("rmm-remote %s starting; relay=%s", remoteVersion, *relay)

	conn, _, err := websocket.DefaultDialer.Dial(*relay, nil)
	if err != nil {
		log.Fatalf("rmm-remote: dial relay: %v", err)
	}
	defer conn.Close()

	if err := runMedia(conn); err != nil {
		log.Printf("rmm-remote: media session ended: %v", err)
	}
}

const remoteVersion = "0.5.0"
