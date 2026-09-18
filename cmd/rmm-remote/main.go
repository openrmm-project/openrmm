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
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorilla/websocket"
)

func main() {
	relay := flag.String("relay", os.Getenv("RMM_RELAY_URL"), "relay WebSocket URL (role=agent)")
	ticket := flag.String("ticket", os.Getenv("RMM_RELAY_TICKET"), "single-use relay ticket")
	printVer := flag.Bool("print-version", false, "print the sidecar version and exit")
	filesOnly := flag.Bool("files", false, "file-transfer-only mode: no screen capture/streaming")
	noFiles := flag.Bool("no-files", false, "disable in-band file transfer in desktop mode")
	session := flag.String("session", "", "read the session parameters from this file and delete it (macOS LaunchAgent)")
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
	openLog()
	// Before the check below, not after: this is what supplies the relay URL.
	// A LaunchAgent's plist is static while a relay URL and a single-use ticket
	// are not, so the agent leaves them in a file owned by the session user and
	// kickstarts the job. Reading them here also keeps the ticket out of the
	// process arguments, where `ps` shows it to every user on the machine.
	if *session != "" {
		p, err := readSessionFile(*session)
		if err != nil {
			log.Fatalf("rmm-remote: session file: %v", err)
		}
		*relay, *ticket, *filesOnly, *noFiles = p.Relay, p.Ticket, p.FilesOnly, p.NoFiles
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

const remoteVersion = "0.15.0"

// sessionParams is what the agent hands to a LaunchAgent-started sidecar.
type sessionParams struct {
	Relay     string `json:"relay"`
	Ticket    string `json:"ticket"`
	FilesOnly bool   `json:"files_only"`
	NoFiles   bool   `json:"no_files"`
}

// readSessionFile consumes the file: the ticket is single-use, and leaving it on
// disk after the session starts would widen the window in which it is stealable
// beyond the moment it is actually needed.
func readSessionFile(path string) (sessionParams, error) {
	var p sessionParams
	b, err := os.ReadFile(path)
	_ = os.Remove(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if p.Relay == "" {
		return p, fmt.Errorf("no relay URL")
	}
	return p, nil
}

// openLog points the logger at a file, because the sidecar is started with no
// console: on Windows with CREATE_NO_WINDOW, and by launchd or a service with
// stdout on /dev/null.
//
// The fallback is not decoration. The same binary runs as root for a
// file-transfer session and as the console user for a desktop one, so a log
// created by the first is unwritable by the second — which is how a macOS
// session came to leave no trace at all, not even its first line, while we were
// looking for a capture problem that did not exist.
func openLog() {
	paths := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		// Never inside an .app. A signed bundle seals its contents, so adding a
		// file invalidates the signature — and on macOS an invalid signature
		// costs every privacy grant the bundle holds. Writing one log line there
		// turned a working session back into a black screen.
		if !strings.Contains(filepath.ToSlash(dir), ".app/Contents/") {
			paths = append(paths, filepath.Join(dir, "rmm-remote.log"))
		}
	}
	// Exists on macOS only, which is the platform that needs somewhere better
	// than the temp directory; elsewhere the check simply fails and is skipped.
	if home, err := os.UserHomeDir(); err == nil {
		logs := filepath.Join(home, "Library", "Logs")
		if info, err := os.Stat(logs); err == nil && info.IsDir() {
			paths = append(paths, filepath.Join(logs, "rmm-remote.log"))
		}
	}
	paths = append(paths, filepath.Join(os.TempDir(), "rmm-remote.log"))
	for _, p := range paths {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666); err == nil {
			log.SetOutput(f)
			return
		}
	}
}
