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
// transfer (transfer_linux.go). Chat is still Windows-only.
func runMedia(conn *websocket.Conn) error {
	type omsg struct {
		mt int
		b  []byte
	}
	out := make(chan omsg, 64)
	inputCh := make(chan []byte, 256)
	done := make(chan struct{})
	defer close(done)
	errc := make(chan error, 3)
	var kfReq atomic.Bool

	// Input via /dev/uinput (kernel level): works on the login greeter and any
	// session, Xorg or Wayland. Falls back to XTEST (X11 only) if unavailable.
	inp, uerr := newUinput()
	if uerr != nil {
		log.Printf("rmm-remote: uinput unavailable (%v) — input via XTEST only (X11; login screen may reject input). Try: sudo modprobe uinput", uerr)
	} else {
		log.Printf("rmm-remote: input via /dev/uinput (works on the login screen and any session)")
		defer inp.close()
	}

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

	// File transfer rides the same relay (JSON control + [chFile][id][bytes]).
	xfer := newFileXfer(func(mt int, b []byte) {
		select {
		case out <- omsg{mt, b}:
		case <-done:
		}
	})

	// announce the running sidecar version (viewer staleness check)
	if b, e := json.Marshal(map[string]any{"t": "hello", "v": remoteVersion}); e == nil {
		out <- omsg{websocket.TextMessage, b}
	}

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
			switch e.T {
			case "m", "b", "w", "k":
				select {
				case inputCh <- data:
				default: // drop under backpressure (mouse-move spam)
				}
			case "kf":
				kfReq.Store(true)
			default:
				xfer.dispatch(e.T, data) // file-transfer control (fls/fget/fput/…)
			}
		}
	}()

	// Capture + input + seat-following on one goroutine so all X11 socket access
	// is serialized and the connection can be swapped on a session change.
	go func() {
		enc := newVideoEncoder(65)
		var x *x11
		var curKey string
		var lastEval time.Time
		var freshAttach bool
		var attachedAt time.Time
		var noticeSent, loggedOK bool
		defer func() {
			if x != nil {
				x.close()
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
			enc.reset()
			freshAttach = true
			log.Printf("rmm-remote: attached x11 %s %dx%d xtest=%v keymap=%d",
				disp, x.width, x.height, x.xtestOp != 0, len(x.keycodeFor))
		}

		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case data := <-inputCh:
				injectInput(x, inp, data)
			case <-ticker.C:
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
						log.Printf("rmm-remote: capturing OK (frame has content)")
					}
					if noticeSent { // content returned — clear the banner
						noticeSent = false
						sendNotice("")
					}
				}
				payload, err := enc.encode(img)
				if err != nil || payload == nil {
					continue
				}
				select {
				case out <- omsg{websocket.BinaryMessage, append([]byte{chVideo}, payload...)}:
				case <-done:
					return
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

// injectInput delivers one viewer event using uinput when available (works on
// the login greeter + any session), otherwise XTEST (X11 only).
func injectInput(x *x11, u *uinputDevice, data []byte) {
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
