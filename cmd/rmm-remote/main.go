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
//
//	agent  -> viewer : binary message = a tile-delta video frame (encoder.go)
//	viewer -> agent  : text message   = a JSON input event, one of
//	    {"t":"m","x":0.5,"y":0.5}                 mouse move (normalized 0..1)
//	    {"t":"b","x":..,"y":..,"button":0,"down":true}  mouse button
//	    {"t":"w","dy":120}                        wheel (browser deltaY)
//	    {"t":"k","code":65,"down":true}           key (browser keyCode ~= VK)
//	    {"t":"cad"}                                   Windows secure attention sequence
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"
)

func main() {
	relay := flag.String("relay", os.Getenv("RMM_RELAY_URL"), "relay WebSocket URL (role=agent)")
	ticket := flag.String("ticket", os.Getenv("RMM_RELAY_TICKET"), "single-use relay ticket")
	printVer := flag.Bool("print-version", false, "print the sidecar version and exit")
	filesOnly := flag.Bool("files", false, "file-transfer-only mode: no screen capture/streaming")
	noFiles := flag.Bool("no-files", false, "disable in-band file transfer in desktop mode")
	flag.Parse()
	if *printVer {
		fmt.Println(remoteVersion)
		return
	}
	// The sidecar is launched with CREATE_NO_WINDOW, so log to a file next to the
	// binary for diagnostics (why a session ends, capture errors, ...).
	// Microsecond timestamps make the startup breakdown (dial → X11 attach →
	// first frame) legible when chasing "time to first frame".
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if exe, err := os.Executable(); err == nil {
		if f, e := os.OpenFile(filepath.Join(filepath.Dir(exe), "rmm-remote.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); e == nil {
			log.SetOutput(f)
		}
	}
	if *relay == "" {
		log.Fatal("rmm-remote: -relay (or RMM_RELAY_URL) is required")
	}
	log.Printf("rmm-remote %s starting", remoteVersion)

	var headers http.Header
	if *ticket != "" {
		headers = http.Header{"Authorization": []string{"Remote-Agent " + *ticket}}
	} else {
		// Agents released before the ticket handshake pass their one-time agent
		// ticket in the legacy relay URL. Keep this path only for rolling upgrades;
		// current agents always send the ticket in Authorization.
		log.Printf("rmm-remote: using legacy relay handshake during agent upgrade")
	}
	conn, _, err := websocket.DefaultDialer.Dial(*relay, headers)
	if err != nil {
		log.Fatalf("rmm-remote: dial relay: %v", err)
	}
	defer conn.Close()

	run := runMedia
	if *filesOnly {
		run = runFilesOnly // file transfer without screen capture
	}
	allowFiles := !*noFiles
	if *filesOnly {
		allowFiles = true
	}
	if err := runPlatformUI(!*filesOnly, func() error { return run(conn, allowFiles) }); err != nil {
		log.Printf("rmm-remote: session ended: %v", err)
	}
}

const remoteVersion = "0.12.0"
