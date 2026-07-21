//go:build darwin

package main

// macOS (CoreGraphics) media engine. This is the ONLY part of the project that
// uses CGO — and only when building for darwin: capture (CGDisplayCreateImage)
// and input (CGEvent) live in Apple frameworks with no socket/syscall fallback,
// unlike the Windows GDI (DLL syscalls) and Linux X11 (raw protocol) engines,
// which stay CGO-free. The build tag isolates it: Linux/Windows builds never
// see this file and remain static, cross-compilable, CGO_ENABLED=0.
//
// It feeds the SAME shared tile-delta encoder (encoder.go) and speaks the same
// relay wire protocol as the other engines, so the same viewer drives it. File
// transfer is the shared POSIX implementation (transfer_posix.go).
//
// OPERATIONAL CAVEATS (macOS-specific, enforced by the OS — document, don't
// silently fail):
//   - Screen capture requires the "Screen Recording" TCC permission for the
//     binary (System Settings ▸ Privacy & Security). Without it, frames are
//     blank/desktop-only. Running as root does NOT bypass TCC on modern macOS.
//   - Input injection requires the "Accessibility" TCC permission.
//   - The login window (loginwindow) cannot be captured without special
//     entitlements; this engine targets the active user session.
//   - Retina displays report points vs pixels; CGImage gives pixels and CGEvent
//     takes points — the capturer sizes the frame from the image and scales
//     pointer coordinates from the same image dims (validate on a Retina Mac).
//   - In-session chat (the Win32/X11 chat window) is NOT yet ported to macOS;
//     it needs a Cocoa window. Tracked as follow-up.

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>

// CGEventCreateScrollWheelEvent is variadic (trailing wheel2/wheel3 deltas),
// which cgo cannot call directly (Go 1.25 cgo rejects it: "unexpected type: ...").
// Wrap the single-wheel form in a non-variadic shim.
static CGEventRef rmmCreateScrollWheelEvent(CGEventSourceRef source, CGScrollEventUnit units, uint32_t wheelCount, int32_t wheel1) {
	return CGEventCreateScrollWheelEvent(source, units, wheelCount, wheel1);
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
)

// cgCapturer captures the main display via CoreGraphics. The frame buffer is
// reused across calls and reallocated only when the resolution changes.
type cgCapturer struct {
	display C.CGDirectDisplayID
	w, h    int
	img     *image.RGBA
}

func newCGCapturer() (*cgCapturer, error) {
	d := C.CGMainDisplayID()
	return &cgCapturer{display: d}, nil
}

// frame grabs the current screen into an RGBA image. CGImage pixels are BGRA
// (kCGBitmapByteOrder32Little + premultiplied alpha); we convert to RGBA and
// honor the per-row stride (bytesPerRow may exceed width*4 for alignment).
func (c *cgCapturer) frame() (*image.RGBA, error) {
	cgImg := C.CGDisplayCreateImage(c.display)
	if cgImg == nil {
		return nil, fmt.Errorf("CGDisplayCreateImage failed (Screen Recording permission?)")
	}
	defer C.CGImageRelease(cgImg)

	w := int(C.CGImageGetWidth(cgImg))
	h := int(C.CGImageGetHeight(cgImg))
	stride := int(C.CGImageGetBytesPerRow(cgImg))
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("empty display image")
	}
	if c.img == nil || c.w != w || c.h != h {
		c.w, c.h = w, h
		c.img = image.NewRGBA(image.Rect(0, 0, w, h))
	}

	provider := C.CGImageGetDataProvider(cgImg)
	data := C.CGDataProviderCopyData(provider)
	if data == nil {
		return nil, fmt.Errorf("CGDataProviderCopyData failed")
	}
	defer C.CFRelease(C.CFTypeRef(data))
	src := C.CFDataGetBytePtr(data)
	srcLen := int(C.CFDataGetLength(data))

	dst := c.img.Pix
	for y := 0; y < h; y++ {
		row := y * stride
		if row+w*4 > srcLen {
			break
		}
		// Read one row straight from the C buffer (no per-pixel cgo call).
		rb := C.GoBytes(unsafe.Pointer(uintptr(unsafe.Pointer(src))+uintptr(row)), C.int(w*4))
		di := y * c.img.Stride
		for x := 0; x < w; x++ {
			si := x * 4
			dst[di] = rb[si+2]   // R  (from B-G-R-A)
			dst[di+1] = rb[si+1] // G
			dst[di+2] = rb[si]   // B
			dst[di+3] = 255      // A (opaque)
			di += 4
		}
	}
	return c.img, nil
}

// ---- input injection (CGEvent) ----

func postMouseMoveDrag(x, y float64, w, h int, buttonsDown int) {
	pt := C.CGPointMake(C.double(x*float64(w)), C.double(y*float64(h)))
	typ := C.kCGEventMouseMoved
	btn := C.kCGMouseButtonLeft
	if buttonsDown&1 != 0 {
		typ = C.kCGEventLeftMouseDragged
	} else if buttonsDown&2 != 0 {
		typ = C.kCGEventRightMouseDragged
		btn = C.kCGMouseButtonRight
	}
	e := C.CGEventCreateMouseEvent(nil, C.CGEventType(typ), pt, C.CGMouseButton(btn))
	C.CGEventPost(C.kCGHIDEventTap, e)
	C.CFRelease(C.CFTypeRef(e))
}

func postMouseButton(x, y float64, w, h, button int, down bool) {
	pt := C.CGPointMake(C.double(x*float64(w)), C.double(y*float64(h)))
	var typ C.CGEventType
	var btn C.CGMouseButton
	switch button {
	case 2: // right
		btn = C.kCGMouseButtonRight
		if down {
			typ = C.kCGEventRightMouseDown
		} else {
			typ = C.kCGEventRightMouseUp
		}
	case 1: // middle
		btn = C.kCGMouseButtonCenter
		if down {
			typ = C.kCGEventOtherMouseDown
		} else {
			typ = C.kCGEventOtherMouseUp
		}
	default: // left
		btn = C.kCGMouseButtonLeft
		if down {
			typ = C.kCGEventLeftMouseDown
		} else {
			typ = C.kCGEventLeftMouseUp
		}
	}
	e := C.CGEventCreateMouseEvent(nil, typ, pt, btn)
	C.CGEventPost(C.kCGHIDEventTap, e)
	C.CFRelease(C.CFTypeRef(e))
}

func postScroll(dy float64) {
	amt := C.int32_t(-3)
	if dy < 0 {
		amt = 3
	}
	e := C.rmmCreateScrollWheelEvent(nil, C.kCGScrollEventUnitLine, 1, amt)
	C.CGEventPost(C.kCGHIDEventTap, e)
	C.CFRelease(C.CFTypeRef(e))
}

func postKey(browserCode int, down bool) {
	kc, ok := browserKeyMac[browserCode]
	if !ok {
		return
	}
	e := C.CGEventCreateKeyboardEvent(nil, C.CGKeyCode(kc), C.bool(down))
	C.CGEventPost(C.kCGHIDEventTap, e)
	C.CFRelease(C.CFTypeRef(e))
}

// ---- relay loop ----

func runMedia(conn *websocket.Conn, allowFiles bool) error {
	type omsg struct {
		mt int
		b  []byte
	}
	out := make(chan omsg, 64)
	fileOut := make(chan omsg, 16)
	inputCh := make(chan []byte, 256)
	done := make(chan struct{})
	defer close(done)
	errc := make(chan error, 3)
	var kfReq atomic.Bool
	pace := newPacer()

	// single writer; video/control (out) takes strict priority over file traffic.
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

	xfer := newFileXfer(func(mt int, b []byte) {
		select {
		case fileOut <- omsg{mt, b}:
		case <-done:
		}
	})

	if b, e := json.Marshal(map[string]any{"t": "hello", "v": remoteVersion, "allow_files": allowFiles}); e == nil {
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
			case "m", "b", "w", "k":
				select {
				case inputCh <- data:
				default:
				}
			case "kf":
				kfReq.Store(true)
				pace.markActivity()
			default:
				if allowFiles {
					xfer.dispatch(e.T, data)
				}
			}
		}
	}()

	cap, err := newCGCapturer()
	if err != nil {
		return err
	}

	// Input runs on the same goroutine as capture for simplicity; CGEvent is
	// thread-safe but keeping it serialized avoids surprising interleavings.
	enc := newVideoEncoder(jpegQuality())
	ticker := time.NewTicker(pace.active)
	defer ticker.Stop()
	curInterval := pace.active
	var buttons int
	for {
		select {
		case <-done:
			return <-errc
		case data := <-inputCh:
			pace.markActivity()
			injectMacInput(data, cap.w, cap.h, &buttons)
		case <-ticker.C:
			if len(out) >= videoBacklog {
				continue
			}
			if kfReq.Swap(false) {
				enc.forceKeyframe()
			}
			img, err := cap.frame()
			if err != nil {
				log.Printf("rmm-remote: capture: %v", err)
				time.Sleep(500 * time.Millisecond)
				continue
			}
			payload, eerr := enc.encode(img)
			sent := false
			if eerr == nil && payload != nil {
				select {
				case out <- omsg{websocket.BinaryMessage, append([]byte{chVideo}, payload...)}:
					sent = true
				case <-done:
					return <-errc
				}
			}
			if iv := pace.interval(sent); iv != curInterval {
				curInterval = iv
				ticker.Reset(iv)
			}
		}
	}
}

type macInput struct {
	T      string  `json:"t"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Button int     `json:"button"`
	Down   bool    `json:"down"`
	DY     float64 `json:"dy"`
	Code   int     `json:"code"`
}

func injectMacInput(data []byte, w, h int, buttons *int) {
	var e macInput
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch e.T {
	case "m":
		postMouseMoveDrag(e.X, e.Y, w, h, *buttons)
	case "b":
		mask := 1 << uint(min(e.Button, 2))
		if e.Down {
			*buttons |= mask
		} else {
			*buttons &^= mask
		}
		postMouseButton(e.X, e.Y, w, h, e.Button, e.Down)
	case "w":
		postScroll(e.DY)
	case "k":
		postKey(e.Code, e.Down)
	}
}
