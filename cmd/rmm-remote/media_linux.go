//go:build linux

package main

import (
	"encoding/json"
	"log"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// runMedia is the Linux (X11) media engine with seat-following: it attaches to
// whichever X server owns the active VT — the display-manager greeter before
// login, the user's session after — and re-attaches as that changes, so remote
// works on the login screen and survives logout/lock (the Linux analogue of the
// Windows desktop-following). It mirrors the Windows wire behavior (hello +
// chVideo tile frames + {t:"kf"}) so the same viewer drives both, including file
// transfer (transfer_linux.go) and in-session chat (a native X11 window,
// chat_linux.go).
func runMedia(conn *websocket.Conn, allowFiles bool) error {
	clipMirror := &clipboardMirror{}
	type omsg struct {
		mt int
		b  []byte
	}
	out := make(chan omsg, 64)
	fileOut := make(chan omsg, 16)
	inputCh := make(chan []byte, 256)
	chatInbox := make(chan string, 32) // tech → host chat messages
	done := make(chan struct{})
	defer close(done)
	errc := make(chan error, 3)
	var kfReq atomic.Bool
	pace := newPacer()

	// Monitor selection: 0 is the whole X screen (all monitors, as RandR
	// stitched them), 1..N one display. The reader only records the wish; the
	// capture goroutine owns *x11 and applies it.
	reg := &captureRegion{}
	var monSel atomic.Int32
	var monReq atomic.Bool

	// Input via /dev/uinput (kernel level): works on the login greeter and any
	// session, Xorg or Wayland. Falls back to XTEST (X11 only) if unavailable.
	inp, uerr := newUinput()
	if uerr != nil {
		log.Printf("rmm-remote: uinput unavailable (%v) — input via XTEST only (X11; login screen may reject input). Try: sudo modprobe uinput", uerr)
	} else {
		log.Printf("rmm-remote: input via /dev/uinput (works on the login screen and any session)")
		defer inp.close()
	}

	// With uinput, inject input on its own goroutine: uinput writes never touch
	// the X socket, so the cursor keeps moving even while the capture goroutine
	// is busy inside a JPEG encode (this was the main source of input freezes).
	// The XTEST fallback shares the X connection and must stay on the capture
	// goroutine — xtestCh is nil (never selected) when uinput owns the input.
	xtestCh := inputCh
	if inp != nil {
		xtestCh = nil
		go func() {
			for {
				select {
				case <-done:
					return
				case data := <-inputCh:
					pace.markActivity()
					injectInput(nil, inp, data, reg)
				}
			}
		}()
	}

	// single writer (gorilla forbids concurrent writes). Video/control frames
	// (out) take strict priority over file-transfer traffic (fileOut), so a bulk
	// download can't queue ahead of the screen and freeze the session.
	write := func(m omsg) bool {
		if err := conn.WriteMessage(m.mt, m.b); err != nil {
			errc <- err
			return false
		}
		return true
	}
	go func() {
		for {
			select {
			case <-done:
				return
			case m := <-out:
				if !write(m) {
					return
				}
			default:
				select {
				case <-done:
					return
				case m := <-out:
					if !write(m) {
						return
					}
				case m := <-fileOut:
					if !write(m) {
						return
					}
				}
			}
		}
	}()

	// File transfer rides the same relay (JSON control + [chFile][id][bytes])
	// but queues on its own lane (see the writer above).
	xfer := newFileXfer(func(mt int, b []byte) {
		select {
		case fileOut <- omsg{mt, b}:
		case <-done:
		}
	})

	// Physical-input lock (evdev EVIOCGRAB). Idle until a viewer enables it;
	// always released when the session ends (kernel also frees grabs on fd close).
	lock := &inputLock{}
	defer lock.closeAll()

	sendMonitors := func(x *x11) {
		var list []map[string]any
		if mons := x.monitors(); len(mons) > 1 {
			for i, m := range mons {
				list = append(list, map[string]any{
					"id": i + 1, "w": m.W, "h": m.H, "primary": m.Primary,
				})
			}
		}
		if b, e := json.Marshal(map[string]any{"t": "monitors", "list": list, "selected": monSel.Load()}); e == nil {
			select {
			case out <- omsg{websocket.TextMessage, b}:
			case <-done:
			}
		}
	}

	// announce the running sidecar version (viewer staleness check)
	if b, e := json.Marshal(map[string]any{"t": "hello", "v": remoteVersion, "allow_files": allowFiles, "cad": true, "chat": true}); e == nil {
		out <- omsg{websocket.TextMessage, b}
	}

	// Mirror the endpoint's clipboard to the technician for as long as the
	// session lasts, so a copy on the remote screen can be pasted at home.
	go watchClipboard(clipMirror, func(text string) {
		b, err := json.Marshal(map[string]any{"t": "clip", "text": text})
		if err != nil {
			return
		}
		select {
		case out <- omsg{websocket.TextMessage, b}:
		case <-done:
		}
	}, done)

	// reader / dispatcher
	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if mt == websocket.BinaryMessage {
				// viewer -> agent file upload chunk: [chFile][id][bytes]
				if allowFiles && len(data) >= 2 && data[0] == chFile {
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
			switch e.T {
			case "clip":
				var cm struct {
					Text  string `json:"text"`
					Paste bool   `json:"paste"`
				}
				if json.Unmarshal(data, &cm) == nil && cm.Text != "" {
					clipMirror.remember(cm.Text)
					if err := setClipboard(cm.Text); err != nil {
						log.Printf("rmm-remote: clipboard: %v", err)
					} else if cm.Paste {
						// Sent as ordinary key events so the paste travels
						// through whichever backend is injecting input —
						// uinput or XTEST — instead of a second code path.
						for _, event := range pasteKeyEvents {
							select {
							case inputCh <- event:
							case <-done:
								return
							}
						}
					}
				}
			case "mon":
				var mm struct {
					ID int32 `json:"id"`
				}
				if json.Unmarshal(data, &mm) == nil && mm.ID >= 0 {
					monSel.Store(mm.ID)
					monReq.Store(true)
				}
			case "cad":
				select {
				case inputCh <- data:
					// Dispatched, not confirmed: what the chord does is the
					// desktop's shortcut configuration, not ours.
					if b, e := json.Marshal(map[string]any{"t": "cad", "ok": true}); e == nil {
						select {
						case out <- omsg{websocket.TextMessage, b}:
						case <-done:
						}
					}
				case <-done:
					return
				}
			case "m", "b", "w", "k":
				select {
				case inputCh <- data:
				default: // drop under backpressure (mouse-move spam)
				}
			case "kf":
				kfReq.Store(true)
				pace.markActivity() // viewer (re)connected — resume the active rate
			case "lockinput":
				var lm struct {
					Enable bool `json:"enable"`
				}
				_ = json.Unmarshal(data, &lm)
				go func(on bool) {
					err := lock.set(on)
					ack := map[string]any{"t": "inputlocked", "enabled": on && err == nil}
					if err != nil {
						ack["err"] = err.Error()
						log.Printf("inputlock: %v", err)
					}
					if b, e := json.Marshal(ack); e == nil {
						select {
						case out <- omsg{websocket.TextMessage, b}:
						case <-done:
						}
					}
					if err == nil {
						note := "⚠ The technician locked your keyboard and mouse for this session."
						if !on {
							note = "✓ Your keyboard and mouse have been released."
						}
						select { // transparency notice in the host chat window
						case chatInbox <- note:
						default:
						}
					}
				}(lm.Enable)
			case "chat":
				var cm struct {
					From string `json:"from"`
					Text string `json:"text"`
				}
				if json.Unmarshal(data, &cm) == nil && cm.From == "tech" && cm.Text != "" {
					select {
					case chatInbox <- cm.Text:
					default:
					}
				}
			default:
				if allowFiles {
					xfer.dispatch(e.T, data) // file-transfer control (fls/fget/fput/…)
				}
			}
		}
	}()

	// Capture + seat-following on one goroutine so all X11 socket access is
	// serialized and the connection can be swapped on a session change. XTEST
	// input (the no-uinput fallback) also lands here for the same reason.
	go func() {
		enc := newVideoEncoder(jpegQuality())
		var x *x11
		var curKey string
		var lastEval time.Time
		var freshAttach bool
		var attachedAt time.Time
		var noticeSent, loggedOK bool
		startedAt := time.Now()       // for the "time to first frame" log
		var chat *chatLinux           // native in-session chat window (per X session)
		var chatDisp, chatAuth string // active X display/auth, for reopening the chat
		const chatTitle = "OpenRMM - Chat with technician"
		defer func() {
			if x != nil {
				x.close()
			}
			if chat != nil {
				chat.close()
			}
		}()

		sendNotice := func(text string) {
			if b, e := json.Marshal(map[string]any{"t": "notice", "text": text}); e == nil {
				select {
				case out <- omsg{websocket.TextMessage, b}:
				case <-done:
				}
			}
		}

		// sendChat hands a host-typed reply back to the viewer over the relay's
		// priority lane (out), mirroring the Windows chat window.
		sendChat := func(text string) {
			if b, e := json.Marshal(map[string]any{"t": "chat", "from": "user", "text": text}); e == nil {
				select {
				case out <- omsg{websocket.TextMessage, b}:
				case <-done:
				}
			}
		}

		// reattach connects to the active X server when it differs from the
		// current one (or when we have none); a no-op while the session is stable.
		reattach := func() {
			disp, auth, ok := findActiveX()
			key := ""
			if ok {
				key = disp + "\x00" + auth
			}
			if x != nil && key == curKey {
				return
			}
			if x != nil {
				x.close()
				x = nil
			}
			curKey = key
			if !ok {
				return
			}
			nx, err := newX11At(disp, auth)
			if err != nil {
				log.Printf("rmm-remote: x11 attach %s (auth=%s): %v", disp, auth, err)
				curKey = "" // retry next eval
				return
			}
			x = nx
			x.setRegion(monSel.Load())
			reg.store(x)
			monReq.Store(false)
			sendMonitors(x)
			enc.reset()
			freshAttach = true
			log.Printf("rmm-remote: attached x11 %s %dx%d xtest=%v keymap=%d",
				disp, x.width, x.height, x.xtestOp != 0, len(x.keycodeFor))
			// Open a fresh chat window on the new X server (the old one died with
			// the previous session). Best-effort — nil if the handshake fails.
			chatDisp, chatAuth = disp, auth
			if chat != nil {
				chat.close()
			}
			chat = newChatWindowLinux(disp, auth, chatTitle, sendChat)
		}

		ticker := time.NewTicker(pace.active)
		defer ticker.Stop()
		curInterval := pace.active
		for {
			select {
			case <-done:
				return
			case data := <-xtestCh:
				pace.markActivity()
				injectInput(x, inp, data, reg)
			case msg := <-chatInbox:
				// Reopen the window if the host user closed it (the WM kills the
				// client, so a new tech message brings it back).
				if (chat == nil || chat.isClosed()) && chatDisp != "" {
					if chat != nil {
						chat.close()
					}
					chat = newChatWindowLinux(chatDisp, chatAuth, chatTitle, sendChat)
				}
				if chat != nil {
					chat.append("Tech: " + msg)
				}
			case <-ticker.C:
				// Backpressure: if the writer hasn't drained what we already
				// queued, skip this tick BEFORE paying for capture+encode. The
				// tile diff folds the skipped change into the next frame, so
				// nothing is lost — only latency. (See videoBacklog in
				// tunables.go for why dropping after encode would be wrong.)
				if len(out) >= videoBacklog {
					continue
				}
				// Applied here, on the goroutine that owns *x11: setRegion
				// swaps the frame buffer the capture is about to write into.
				if monReq.Swap(false) && x != nil {
					x.setRegion(monSel.Load())
					reg.store(x)
					enc.reset() // different frame size — start from a keyframe
					sendMonitors(x)
				}
				// Re-evaluate the active session ~1×/s, or immediately if detached.
				if x == nil || time.Since(lastEval) > time.Second {
					lastEval = time.Now()
					reattach()
				}
				if x == nil {
					continue
				}
				if kfReq.Swap(false) {
					enc.forceKeyframe()
				}
				img, err := x.getImage()
				if err != nil {
					x.close()
					x = nil
					curKey = "" // reattach on the next tick (session likely switched)
					continue
				}
				if freshAttach {
					freshAttach = false
					attachedAt = time.Now()
					noticeSent = false
					loggedOK = false
				}
				if allBlack(img.Pix) {
					// A black frame right after (re)attach is normal — the session
					// or greeter is still drawing. Only warn if the screen stays
					// black for a sustained period, which signals a real Wayland
					// session or a blanked/DPMS-off display (not a transition).
					if !noticeSent && time.Since(attachedAt) > 2*time.Second {
						noticeSent = true
						log.Printf("rmm-remote: WARNING screen black for >2s — likely a Wayland session or a blanked/DPMS-off display.")
						sendNotice("Captured screen is black — the host is likely on a Wayland session (or the display is asleep). For Linux, use an Xorg session and an Xorg greeter.")
					}
				} else {
					if !loggedOK {
						loggedOK = true
						log.Printf("rmm-remote: capturing OK (frame has content) — first content %dms after media start", time.Since(startedAt).Milliseconds())
					}
					if noticeSent { // content returned — clear the banner
						noticeSent = false
						sendNotice("")
					}
				}
				payload, err := enc.encode(img)
				sent := false
				if err == nil && payload != nil {
					select {
					case out <- omsg{websocket.BinaryMessage, append([]byte{chVideo}, payload...)}:
						sent = true
					case <-done:
						return
					}
				}
				// Adaptive cadence: drop to the idle rate once the session has
				// been quiet for a while; any sent frame or input restores the
				// active rate (input may arrive on the uinput goroutine, hence
				// the pacer's atomic clock).
				if iv := pace.interval(sent); iv != curInterval {
					curInterval = iv
					ticker.Reset(iv)
				}
			}
		}
	}()

	return <-errc
}

type linInput struct {
	T      string  `json:"t"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Button int     `json:"button"`
	Down   bool    `json:"down"`
	DY     float64 `json:"dy"`
	Code   int     `json:"code"`
}

// regionGeom is the captured rectangle and the screen it sits on. Stored behind
// a single pointer so the uinput goroutine, which injects while the capture
// goroutine may be switching monitors, can never read a half-updated rectangle.
type regionGeom struct{ x, y, w, h, screenW, screenH int }

type captureRegion struct{ v atomic.Pointer[regionGeom] }

func (r *captureRegion) store(x *x11) {
	r.v.Store(&regionGeom{x.originX, x.originY, x.width, x.height, x.screenW, x.screenH})
}

// toScreen maps a viewer coordinate, normalized against the captured region,
// onto the whole X screen — the space both uinput and XTEST inject into.
func (r *captureRegion) toScreen(nx, ny float64) (float64, float64) {
	g := r.v.Load()
	if g == nil || g.screenW <= 0 || g.screenH <= 0 {
		return nx, ny
	}
	return (float64(g.x) + nx*float64(g.w)) / float64(g.screenW),
		(float64(g.y) + ny*float64(g.h)) / float64(g.screenH)
}

// injectInput delivers one viewer event using uinput when available (works on
// the login greeter + any session), otherwise XTEST (X11 only).
func injectInput(x *x11, u *uinputDevice, data []byte, reg *captureRegion) {
	var command struct {
		T string `json:"t"`
	}
	if json.Unmarshal(data, &command) == nil && command.T == "cad" {
		shortcutKeys([]int{17, 18, 46}, func(code int, down bool) {
			b, _ := json.Marshal(linInput{T: "k", Code: code, Down: down})
			injectInput(x, u, b, reg)
		})
		return
	}
	// Pointer coordinates arrive normalized against the captured region.
	if e := new(linInput); json.Unmarshal(data, e) == nil && (e.T == "m" || e.T == "b") {
		e.X, e.Y = reg.toScreen(e.X, e.Y)
		if b, err := json.Marshal(e); err == nil {
			data = b
		}
	}
	if u == nil {
		if x != nil {
			x.handleInput(data)
		}
		return
	}
	var e linInput
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch e.T {
	case "m":
		u.moveAbs(e.X, e.Y)
	case "b":
		u.moveAbs(e.X, e.Y)
		switch e.Button {
		case 0:
			u.button(btnLeft, e.Down)
		case 1:
			u.button(btnMiddle, e.Down)
		case 2:
			u.button(btnRight, e.Down)
		}
	case "w":
		u.wheel(e.DY < 0)
	case "k":
		if kc, ok := browserKeyEvdev[e.Code]; ok {
			u.key(kc, e.Down)
		}
	}
}

// handleInput injects one viewer event via XTEST (runs on the capture goroutine).
func (x *x11) handleInput(data []byte) {
	var e linInput
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch e.T {
	case "m":
		x.fakeMotion(int(e.X*float64(x.width)), int(e.Y*float64(x.height)))
	case "b":
		// Move to the point first so the click lands where the viewer aimed.
		x.fakeMotion(int(e.X*float64(x.width)), int(e.Y*float64(x.height)))
		switch e.Button {
		case 0:
			x.fakeButton(1, e.Down) // left
		case 1:
			x.fakeButton(2, e.Down) // middle
		case 2:
			x.fakeButton(3, e.Down) // right
		}
	case "w":
		// Wheel: X buttons 4 (up) / 5 (down), press+release per notch.
		btn := byte(5)
		if e.DY < 0 {
			btn = 4
		}
		x.fakeButton(btn, true)
		x.fakeButton(btn, false)
	case "k":
		if kc, ok := x.keycodeForBrowser(e.Code); ok {
			x.fakeKey(kc, e.Down)
		}
	}
}

// allBlack samples an RGBA buffer and reports whether every sampled pixel is
// pure black — a strong hint of a Wayland session (X11 GetImage sees nothing)
// or a blanked/DPMS-off display.
func allBlack(pix []byte) bool {
	const step = 4 * 257 // sample roughly every 257th pixel
	for i := 0; i+2 < len(pix); i += step {
		if pix[i] != 0 || pix[i+1] != 0 || pix[i+2] != 0 {
			return false
		}
	}
	return true
}

// Control-V as the viewer would send it: browser key codes, which both input
// backends already map.
var pasteKeyEvents = [][]byte{
	[]byte(`{"t":"k","code":17,"down":true}`),
	[]byte(`{"t":"k","code":86,"down":true}`),
	[]byte(`{"t":"k","code":86,"down":false}`),
	[]byte(`{"t":"k","code":17,"down":false}`),
}
