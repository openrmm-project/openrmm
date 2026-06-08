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
// chVideo tile frames + {t:"kf"}) so the same viewer drives both. Chat /
// file-transfer are Windows-only for now; on Linux those controls get no reply.
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
		defer func() {
			if x != nil {
				x.close()
			}
		}()

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
				if x != nil {
					x.handleInput(data)
				}
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
