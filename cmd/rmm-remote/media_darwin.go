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

// How far the chord got. The viewer only ever learns "sent", and a chord that
// posts cleanly without opening Force Quit looks exactly like one that worked,
// so the log keeps the difference: 5 means every event was accepted.
//
// Read through a function, not directly: a static in the cgo preamble is local
// to its translation unit, and referencing the variable from Go asks the linker
// for a symbol that was never exported.
static int rmmForceQuitStep = 0;
static int rmmForceQuitProgress(void) { return rmmForceQuitStep; }

// What CGPreflightPostEventAccess said when the chord was sent. Recorded, not
// obeyed: see rmmInputPreflight below.
static int rmmForceQuitPreflight = 0;
static int rmmForceQuitPreflightResult(void) { return rmmForceQuitPreflight; }

// CGPreflightPostEventAccess is a report, not a gate. In the process that asked
// for Accessibility it keeps answering "denied" after the person at the Mac
// allows it — half an hour later on macOS 15.8 — while the events themselves
// are delivered: mouse and keyboard worked in that very session. Gating the
// shortcuts on it made Command-V and Force Quit fail exactly there. So they are
// always posted, and the answer only goes to the log.
static int rmmInputPreflight(void) { return CGPreflightPostEventAccess() ? 1 : 0; }

// Command-Option-Escape opens Force Quit Applications. The shortcut is matched
// against the modifier state the WindowServer maintains, and that state is fed
// by the modifier keys' OWN events: decorating Escape with flags leaves it
// untouched, so the shortcut never fires while the call still reports success.
// Press and release them for real, in order.
static int rmmForceQuit(void) {
    rmmForceQuitStep = 1;
    rmmForceQuitPreflight = rmmInputPreflight();
    rmmForceQuitStep = 2;
    CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
    if (!src) return 0;
    rmmForceQuitStep = 3;
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
    rmmForceQuitStep = ok ? 5 : 4;
    return ok;
}

// Command-V after the pasteboard is replaced. The technician presses whatever
// paste shortcut their own machine uses; what has to arrive here is the one
// this platform understands, so the viewer never guesses the endpoint's OS.
static int rmmPasteShortcut(void) {
    CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
    if (!src) return 0;
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

// Implemented in chat_darwin.m so both permission requests run on AppKit's
// initial process thread. Calling them from this media goroutine can update TCC
// without putting a consent prompt on the logged-in user's desktop.
int rmmRequestScreenCapture(void);
int rmmRequestPostEvents(void);

// Checks only, never a prompt: safe from any thread, and used to watch for a
// grant given while the session is open.
static int rmmScreenGranted(void) { return CGPreflightScreenCaptureAccess() ? 1 : 0; }
static int rmmInputGranted(void) { return rmmInputPreflight(); }
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
		// Two different faults fail the same way. Most often it is not the
		// permission at all: a Mac whose display went to sleep or locked stops
		// yielding images, and blaming Screen Recording sent the search the wrong
		// way on a Mac that had it allowed all along.
		if C.rmmScreenGranted() != 0 {
			return nil, fmt.Errorf("CGDisplayCreateImage failed with Screen Recording allowed: the display is probably asleep or locked")
		}
		return nil, fmt.Errorf("CGDisplayCreateImage failed: Screen Recording is not allowed for this session")
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
	// Asked before the first frame is captured: a grant given now applies to the
	// next session, since macOS does not hand capture rights to a process that
	// has already been refused them.
	clipMirror := &clipboardMirror{}
	// Mirror the endpoint's clipboard to the technician for as long as the
	// session lasts, so a copy on the remote screen can be pasted at home.
	go watchClipboard(clipMirror, func(text string) {
		sendJSON(map[string]any{"t": "clip", "text": text})
	}, done)
	probe := permissionProbe{
		screenGranted: func() bool { return C.rmmScreenGranted() != 0 },
		inputGranted:  func() bool { return C.rmmInputGranted() != 0 },
		requestScreen: func() bool { return C.rmmRequestScreenCapture() != 0 },
		requestInput:  func() bool { return C.rmmRequestPostEvents() != 0 },
	}
	perm := startPermissions(probe)
	log.Printf("rmm-remote: permissions at session start: %s", describePermissions(perm))
	// A deferred Accessibility request is not missing yet: pointing the person
	// at the Accessibility pane now would send them to a list the app is not in.
	showMacPermissionNotice(perm.screen, perm.input || perm.inputDeferred)
	if b, e := json.Marshal(macHello(remoteVersion, allowFiles, perm.screen, perm.input, perm.inputDeferred)); e == nil {
		out <- omsg{websocket.TextMessage, b}
	}
	go followInput(probe, perm, done, time.Second, time.Minute, func(ev permissionEvent) {
		screen := probe.screenGranted()
		switch ev.kind {
		case "screen-allowed":
			log.Printf("rmm-remote: Screen Recording allowed %s into the session; capture is live", ev.after.Round(time.Second))
			sendJSON(macHello(remoteVersion, allowFiles, true, false, true))
		case "asked":
			log.Printf("rmm-remote: asked for Accessibility %s after the session started (Screen Recording allowed first: %t)", ev.after.Round(time.Second), ev.screenAnswered)
		case "granted":
			log.Printf("rmm-remote: Accessibility was already allowed when asked %s into the session; keyboard and mouse are live", ev.after.Round(time.Second))
			sendJSON(macHello(remoteVersion, allowFiles, screen, true, false))
		}
	})
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
					clipMirror.remember(cm.Text)
					if !setMacClipboard(cm.Text) {
						sendJSON(map[string]any{"t": "paste", "ok": false, "err": "Could not write the macOS clipboard."})
						continue
					}
					if cm.Paste {
						// Clipboard write is synchronous; serialize Command-V with
						// every other input event instead of racing a fixed delay.
						select {
						case inputCh <- []byte(`{"t":"paste"}`):
						case <-done:
							return
						}
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
	// A missing Screen Recording grant fails every frame; summarised, it no
	// longer grows the log by megabytes while the session waits for it.
	captureLog := newRepeatLog(time.Minute, log.Printf)
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
			if json.Unmarshal(data, &command) == nil && command.T == "paste" {
				ok := C.rmmPasteShortcut() != 0
				response := map[string]any{"t": "paste", "ok": ok}
				if !ok {
					response["err"] = "The Mac did not accept the Command-V events."
				}
				if C.rmmInputPreflight() == 0 {
					log.Printf("rmm-remote: Command-V posted=%t while macOS reports Accessibility as missing (stale in the process that asked for it)", ok)
				}
				sendJSON(response)
				continue
			}
			if command.T == "cad" {
				ok := C.rmmForceQuit() != 0
				// Logged on both outcomes: the chord can post cleanly and still
				// not open Force Quit, and from the viewer that is
				// indistinguishable from success. Step 5 means every event was
				// accepted, so anything wrong after it is the WindowServer's
				// handling of the shortcut, not ours.
				log.Printf("rmm-remote: force quit chord posted=%t (step %d, Accessibility reported %t)", ok, int(C.rmmForceQuitProgress()), C.rmmForceQuitPreflightResult() != 0)
				response := map[string]any{"t": "cad", "ok": ok}
				if !ok {
					response["err"] = "The Mac did not accept the ⌘⌥Esc events."
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
				captureLog.fail("rmm-remote: capture: " + err.Error())
				time.Sleep(500 * time.Millisecond)
				continue
			}
			captureLog.ok()
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
