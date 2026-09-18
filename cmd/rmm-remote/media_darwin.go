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
//   - In-session chat uses Cocoa on the application's main thread.

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>
#include <stdint.h>
#include <stdbool.h>

// Go 1.25 cgo is strict about CoreFoundation/CoreGraphics opaque types: a NULL
// event source can't be passed as an untyped Go nil, and CGEventCreateScrollWheel
// Event is variadic (which cgo cannot call at all). Handle NULL and the variadic
// call inside C, where the semantics are unambiguous, and expose non-variadic
// shims plus null predicates to Go.
static CGEventRef rmmCreateScrollWheelEvent(CGScrollEventUnit units, uint32_t wheelCount, int32_t wheel1) {
	return CGEventCreateScrollWheelEvent(NULL, units, wheelCount, wheel1);
}
static CGEventRef rmmCreateMouseEvent(CGEventType type, CGPoint pos, CGMouseButton button) {
	return CGEventCreateMouseEvent(NULL, type, pos, button);
}
static CGEventRef rmmCreateKeyboardEvent(CGKeyCode key, bool down) {
	return CGEventCreateKeyboardEvent(NULL, key, down);
}
static int rmmImageIsNull(CGImageRef img) { return img == NULL; }
static int rmmDataIsNull(CFDataRef data) { return data == NULL; }
// CGGetActiveDisplayList is called twice in the usual idiom; a fixed cap keeps
// the Go side allocation-free and no Mac has sixteen displays.
static unsigned int rmmDisplays(unsigned int max, CGDirectDisplayID *out) {
    uint32_t n = 0;
    if (CGGetActiveDisplayList(max, out, &n) != kCGErrorSuccess) return 0;
    return (unsigned int)n;
}
static int rmmPostKey(CGEventSourceRef src, CGKeyCode key, bool down, CGEventFlags flags) {
    CGEventRef e = CGEventCreateKeyboardEvent(src, key, down);
    if (!e) return 0;
    CGEventSetFlags(e, flags);
    CGEventPost(kCGHIDEventTap, e);
    CFRelease(e);
    return 1;
}

// Command-Option-Escape opens Force Quit Applications. The shortcut is matched
// against the modifier state the WindowServer maintains, and that state is fed
// by the modifier keys' OWN events: decorating Escape with flags leaves it
// untouched, so the shortcut never fires while the call still reports success.
// Press and release them for real, in order.
static int rmmForceQuit(void) {
    if (!CGPreflightPostEventAccess()) return 0;
    CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
    const CGEventFlags cmd = kCGEventFlagMaskCommand;
    const CGEventFlags both = kCGEventFlagMaskCommand | kCGEventFlagMaskAlternate;
    int ok = 1;
    ok &= rmmPostKey(src, 55, true, cmd);   // kVK_Command down
    ok &= rmmPostKey(src, 58, true, both);  // kVK_Option down
    ok &= rmmPostKey(src, 53, true, both);  // kVK_Escape down
    ok &= rmmPostKey(src, 53, false, both);
    // Released unconditionally, never behind an early return: giving up midway
    // would leave Command and Option stuck down on the host user's keyboard.
    ok &= rmmPostKey(src, 58, false, cmd);
    ok &= rmmPostKey(src, 55, false, 0);
    if (src) CFRelease(src);
    return ok;
}

// Command-V after the pasteboard is replaced. The technician presses whatever
// paste shortcut their own machine uses; what has to arrive here is the one
// this platform understands, so the viewer never guesses the endpoint's OS.
static int rmmPasteShortcut(void) {
    if (!CGPreflightPostEventAccess()) return 0;
    CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
    const CGEventFlags cmd = kCGEventFlagMaskCommand;
    int ok = 1;
    ok &= rmmPostKey(src, 55, true, cmd);  // kVK_Command down
    ok &= rmmPostKey(src, 9, true, cmd);   // kVK_ANSI_V down
    ok &= rmmPostKey(src, 9, false, cmd);
    // Released unconditionally: an early return would leave Command stuck down
    // on the user's own keyboard.
    ok &= rmmPostKey(src, 55, false, 0);
    if (src) CFRelease(src);
    return ok;
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"sort"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
)

// macDisplay is one active display. Bounds are in POINTS, the space CGEvent
// works in; the captured image is in PIXELS, which is twice that on Retina.
// Conflating the two is why pointer input needs the bounds and not the image.
type macDisplay struct {
	id         C.CGDirectDisplayID
	x, y, w, h float64
	main       bool
}

// macDisplays lists the active displays left to right, then top to bottom — the
// same ordering the other platforms use, so a selection index means the same
// display everywhere.
func macDisplays() []macDisplay {
	var ids [16]C.CGDirectDisplayID
	n := int(C.rmmDisplays(C.uint(len(ids)), &ids[0]))
	if n == 0 {
		return nil
	}
	main := C.CGMainDisplayID()
	out := make([]macDisplay, 0, n)
	for _, id := range ids[:n] {
		b := C.CGDisplayBounds(id)
		out = append(out, macDisplay{
			id: id,
			x:  float64(b.origin.x), y: float64(b.origin.y),
			w: float64(b.size.width), h: float64(b.size.height),
			main: id == main,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].x != out[j].x {
			return out[i].x < out[j].x
		}
		return out[i].y < out[j].y
	})
	return out
}

// describeMacDisplays is the hello-independent monitors payload. all=false says
// this host cannot stitch every display into one frame: CGDisplayCreateImage is
// per-display, so the viewer must not offer "All displays" here.
func describeMacDisplays(selected int32) map[string]any {
	var list []map[string]any
	displays := macDisplays()
	if len(displays) > 1 {
		for i, d := range displays {
			list = append(list, map[string]any{
				"id": i + 1, "w": int(d.w), "h": int(d.h), "primary": d.main,
			})
		}
	}
	return map[string]any{"t": "monitors", "list": list, "selected": selected, "all": false}
}

// cgCapturer captures one display via CoreGraphics. The frame buffer is reused
// across calls and reallocated only when the resolution changes.
type cgCapturer struct {
	display C.CGDirectDisplayID
	x, y    float64 // display origin in global points
	pw, ph  float64 // display size in points
	w, h    int     // captured image size in pixels
	img     *image.RGBA
}

// newCGCapturer picks display 1..N, falling back to the main one — including
// when sel is 0, which on the other platforms means "all of them stitched" and
// here cannot.
func newCGCapturer(sel int32) (*cgCapturer, error) {
	displays := macDisplays()
	pick := -1
	for i, d := range displays {
		if d.main {
			pick = i
		}
	}
	if sel > 0 && int(sel) <= len(displays) {
		pick = int(sel) - 1
	}
	if pick < 0 {
		return nil, fmt.Errorf("no active display")
	}
	d := displays[pick]
	return &cgCapturer{display: d.id, x: d.x, y: d.y, pw: d.w, ph: d.h}, nil
}

// toPoint maps a viewer coordinate, normalized against the captured display,
// into the global point space CGEventPost expects.
func (c *cgCapturer) toPoint(x, y float64) (float64, float64) {
	if c == nil || c.pw <= 0 || c.ph <= 0 {
		return x, y
	}
	return c.x + x*c.pw, c.y + y*c.ph
}

// frame grabs the current screen into an RGBA image. CGImage pixels are BGRA
// (kCGBitmapByteOrder32Little + premultiplied alpha); we convert to RGBA and
// honor the per-row stride (bytesPerRow may exceed width*4 for alignment).
func (c *cgCapturer) frame() (*image.RGBA, error) {
	cgImg := C.CGDisplayCreateImage(c.display)
	if C.rmmImageIsNull(cgImg) != 0 {
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
	if C.rmmDataIsNull(data) != 0 {
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

func postMouseMoveDrag(px, py float64, buttonsDown int) {
	pt := C.CGPointMake(C.double(px), C.double(py))
	typ := C.kCGEventMouseMoved
	btn := C.kCGMouseButtonLeft
	if buttonsDown&1 != 0 {
		typ = C.kCGEventLeftMouseDragged
	} else if buttonsDown&2 != 0 {
		typ = C.kCGEventRightMouseDragged
		btn = C.kCGMouseButtonRight
	}
	e := C.rmmCreateMouseEvent(C.CGEventType(typ), pt, C.CGMouseButton(btn))
	C.CGEventPost(C.kCGHIDEventTap, e)
	C.CFRelease(C.CFTypeRef(e))
}

func postMouseButton(px, py float64, button int, down bool) {
	pt := C.CGPointMake(C.double(px), C.double(py))
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
	e := C.rmmCreateMouseEvent(typ, pt, btn)
	C.CGEventPost(C.kCGHIDEventTap, e)
	C.CFRelease(C.CFTypeRef(e))
}

func postScroll(dy float64) {
	amt := C.int32_t(-3)
	if dy < 0 {
		amt = 3
	}
	e := C.rmmCreateScrollWheelEvent(C.kCGScrollEventUnitLine, 1, amt)
	C.CGEventPost(C.kCGHIDEventTap, e)
	C.CFRelease(C.CFTypeRef(e))
}

func postKey(browserCode int, down bool) {
	kc, ok := browserKeyMac[browserCode]
	if !ok {
		return
	}
	e := C.rmmCreateKeyboardEvent(C.CGKeyCode(kc), C.bool(down))
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

	// Display selection: 1..N, applied by the capture goroutine that owns the
	// capturer. There is no 0 here — CGDisplayCreateImage is per-display, so
	// this host cannot stitch them the way Windows and Linux do.
	var monSel atomic.Int32
	var monReq atomic.Bool

	sendJSON := func(v any) {
		b, _ := json.Marshal(v)
		select {
		case out <- omsg{websocket.TextMessage, b}:
		case <-done:
		}
	}
	openMacChat(func(text string) { sendJSON(map[string]any{"t": "chat", "from": "user", "text": text}) })
	defer closeMacChat()
	cadAvailable := bool(C.CGPreflightPostEventAccess())
	hello := map[string]any{"t": "hello", "v": remoteVersion, "allow_files": allowFiles, "cad": cadAvailable, "chat": true}
	if !cadAvailable {
		hello["cad_error"] = "Enable Accessibility for rmm-remote in macOS System Settings → Privacy & Security, then reopen the session."
	}
	if b, e := json.Marshal(hello); e == nil {
		out <- omsg{websocket.TextMessage, b}
	}
	sendJSON(describeMacDisplays(monSel.Load()))

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
			case "chat":
				var message struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(data, &message) == nil && message.Text != "" {
					appendMacChat(message.Text, true)
				}
			case "mon":
				var mm struct {
					ID int32 `json:"id"`
				}
				if json.Unmarshal(data, &mm) == nil && mm.ID > 0 {
					monSel.Store(mm.ID)
					monReq.Store(true)
				}
			case "clip":
				var cm struct {
					Text  string `json:"text"`
					Paste bool   `json:"paste"`
				}
				if json.Unmarshal(data, &cm) == nil && cm.Text != "" {
					setMacClipboard(cm.Text)
					if cm.Paste {
						// The pasteboard write is dispatched to the main queue,
						// so the shortcut waits for it: pasting first would
						// paste whatever the Mac held before.
						go func() {
							time.Sleep(120 * time.Millisecond)
							C.rmmPasteShortcut()
						}()
					}
				}
			case "cad":
				select {
				case inputCh <- data:
				case <-done:
					return
				}
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

	cap, err := newCGCapturer(monSel.Load())
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
		case err := <-errc:
			return err
		case data := <-inputCh:
			pace.markActivity()
			var command macInput
			if json.Unmarshal(data, &command) == nil && command.T == "cad" {
				ok := C.rmmForceQuit() != 0
				response := map[string]any{"t": "cad", "ok": ok}
				if !ok {
					response["err"] = "macOS Accessibility permission is required for ⌘⌥Esc."
				}
				sendJSON(response)
				continue
			}
			injectMacInput(data, cap, &buttons)
		case <-ticker.C:
			if len(out) >= videoBacklog {
				continue
			}
			// Applied here, on the goroutine that owns the capturer.
			if monReq.Swap(false) {
				if nc, nerr := newCGCapturer(monSel.Load()); nerr == nil {
					cap = nc
					enc.reset() // different display, different frame size
					sendJSON(describeMacDisplays(monSel.Load()))
				} else {
					log.Printf("rmm-remote: display %d unavailable: %v", monSel.Load(), nerr)
				}
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

// injectMacInput delivers one viewer event. Pointer coordinates arrive
// normalized against the captured display and are mapped to global points
// first: the image is in pixels, which differs from points on Retina, and a
// non-main display does not start at the origin.
func injectMacInput(data []byte, c *cgCapturer, buttons *int) {
	var e macInput
	if json.Unmarshal(data, &e) != nil {
		return
	}
	px, py := c.toPoint(e.X, e.Y)
	switch e.T {
	case "m":
		postMouseMoveDrag(px, py, *buttons)
	case "b":
		mask := 1 << uint(min(e.Button, 2))
		if e.Down {
			*buttons |= mask
		} else {
			*buttons &^= mask
		}
		postMouseButton(px, py, e.Button, e.Down)
	case "w":
		postScroll(e.DY)
	case "k":
		postKey(e.Code, e.Down)
	}
}
