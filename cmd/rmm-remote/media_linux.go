//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// runMedia is the Linux (X11) media engine: capture the root window, encode via
// the shared tile-delta codec, and inject input through XTEST. It deliberately
// mirrors the Windows loop's wire behavior (hello + chVideo frames + {t:"kf"})
// so the same viewer drives both. Chat / file-transfer / UAC are Windows-only
// for now; on Linux those viewer controls simply get no response.
func runMedia(conn *websocket.Conn) error {
	x, err := newX11()
	if err != nil {
		return fmt.Errorf("x11 init: %w", err)
	}
	defer x.close()
	log.Printf("rmm-remote: x11 %dx%d root=%#x xtest=%v keymap=%d",
		x.width, x.height, x.root, x.xtestOp != 0, len(x.keycodeFor))

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

	// capture + input on one goroutine so all X11 socket access is serialized.
	go func() {
		enc := newVideoEncoder(65)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case data := <-inputCh:
				x.handleInput(data)
			case <-ticker.C:
				if kfReq.Swap(false) {
					enc.forceKeyframe()
				}
				img, err := x.getImage()
				if err != nil {
					errc <- err
					return
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
