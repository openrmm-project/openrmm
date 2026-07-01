//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"image"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
)

// ---- Win32 bindings (CGO-free, via lazy DLLs) ----

var (
	user32                = syscall.NewLazyDLL("user32.dll")
	gdi32                 = syscall.NewLazyDLL("gdi32.dll")
	procGetDC             = user32.NewProc("GetDC")
	procReleaseDC         = user32.NewProc("ReleaseDC")
	procGetSystemMetrics  = user32.NewProc("GetSystemMetrics")
	procSendInput         = user32.NewProc("SendInput")
	procSetDPIAware       = user32.NewProc("SetProcessDPIAware")
	procGetCursorInfo     = user32.NewProc("GetCursorInfo")
	procGetIconInfo       = user32.NewProc("GetIconInfo")
	procDrawIconEx        = user32.NewProc("DrawIconEx")
	procOpenInputDesktop  = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop  = user32.NewProc("SetThreadDesktop")
	procCloseDesktop      = user32.NewProc("CloseDesktop")
	procGetUserObjectInfo = user32.NewProc("GetUserObjectInformationW")
	procCreateDC          = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIB         = gdi32.NewProc("CreateDIBSection")
	procSelectObject      = gdi32.NewProc("SelectObject")
	procBitBlt            = gdi32.NewProc("BitBlt")
	procDeleteObject      = gdi32.NewProc("DeleteObject")
	procDeleteDC          = gdi32.NewProc("DeleteDC")
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
	srcCopy           = 0x00CC0020
	captureBlt        = 0x40000000
	dibRGB            = 0

	cursorShowing = 0x0001
	diNormal      = 0x0003

	inputMouse    = 0
	inputKeyboard = 1

	mvMove        = 0x0001
	mvLeftDown    = 0x0002
	mvLeftUp      = 0x0004
	mvRightDown   = 0x0008
	mvRightUp     = 0x0010
	mvMidDown     = 0x0020
	mvMidUp       = 0x0040
	mvWheel       = 0x0800
	mvAbsolute    = 0x8000
	mvVirtualDesk = 0x4000

	keyUp = 0x0002
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type point struct{ X, Y int32 }

type cursorInfo struct {
	cbSize      uint32
	flags       uint32
	hCursor     uintptr
	ptScreenPos point
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	_        uint32 // pad so the handles are 8-byte aligned (amd64)
	hbmMask  uintptr
	hbmColor uintptr
}

// input mirrors the Win32 INPUT struct on amd64 (4-byte type + 4-byte pad +
// 32-byte union). We only build windows-amd64, so this layout is fixed.
type input struct {
	typ uint32
	_   uint32
	u   [32]byte
}

type mouseInput struct {
	dx, dy      int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

// ---- screen capture ----

type capturer struct {
	w, h   int
	vx, vy int32 // virtual-desktop origin (top-left can be negative on multi-monitor)
	screen uintptr
	mem    uintptr
	bitmap uintptr
	pixels []byte
	img    *image.RGBA
}

func newCapturer() (*capturer, error) {
	procSetDPIAware.Call()
	// Capture the whole virtual desktop (all monitors stitched into one image).
	vx, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	vy, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	w, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("could not read screen size (running in session 0?)")
	}
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	mem, _, _ := procCreateDC.Call(screen)

	bi := bitmapInfo{Header: bitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:    int32(w),
		Height:   -int32(h), // negative => top-down rows
		Planes:   1,
		BitCount: 32,
	}}
	var bits *byte
	bitmap, _, _ := procCreateDIB.Call(mem, uintptr(unsafe.Pointer(&bi)), dibRGB, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		procDeleteDC.Call(mem)
		procReleaseDC.Call(0, screen)
		return nil, fmt.Errorf("CreateDIBSection failed")
	}
	procSelectObject.Call(mem, bitmap)

	c := &capturer{
		w: int(w), h: int(h), vx: int32(vx), vy: int32(vy),
		screen: screen, mem: mem, bitmap: bitmap,
		pixels: unsafe.Slice(bits, int(w)*int(h)*4),
		img:    image.NewRGBA(image.Rect(0, 0, int(w), int(h))),
	}
	return c, nil
}

// drawCursor composites the live mouse cursor onto the memory DC (BitBlt does
// not capture it), so the viewer sees the real remote pointer.
func (c *capturer) drawCursor() {
	ci := cursorInfo{cbSize: uint32(unsafe.Sizeof(cursorInfo{}))}
	r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci)))
	if r == 0 || ci.flags != cursorShowing || ci.hCursor == 0 {
		return
	}
	var ii iconInfo
	if r2, _, _ := procGetIconInfo.Call(ci.hCursor, uintptr(unsafe.Pointer(&ii))); r2 == 0 {
		return
	}
	if ii.hbmMask != 0 {
		defer procDeleteObject.Call(ii.hbmMask)
	}
	if ii.hbmColor != 0 {
		defer procDeleteObject.Call(ii.hbmColor)
	}
	x := ci.ptScreenPos.X - c.vx - int32(ii.xHotspot)
	y := ci.ptScreenPos.Y - c.vy - int32(ii.yHotspot)
	procDrawIconEx.Call(c.mem, uintptr(x), uintptr(y), ci.hCursor, 0, 0, 0, 0, diNormal)
}

func (c *capturer) close() {
	if c.bitmap != 0 {
		procDeleteObject.Call(c.bitmap)
	}
	if c.mem != 0 {
		procDeleteDC.Call(c.mem)
	}
	if c.screen != 0 {
		procReleaseDC.Call(0, c.screen)
	}
}

// frame grabs the screen into the capturer's RGBA buffer and returns it. The
// pixels belong to the capturer (reused each call); the tile-delta encoder
// (encoder.go) consumes them and decides what to send. This keeps capture
// OS-specific while encoding stays shared/cross-platform.
func (c *capturer) frame() (*image.RGBA, error) {
	r, _, _ := procBitBlt.Call(c.mem, 0, 0, uintptr(c.w), uintptr(c.h), c.screen, uintptr(c.vx), uintptr(c.vy), srcCopy|captureBlt)
	if r == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}
	// Note: we intentionally do NOT composite the cursor into the frame — that
	// pins the pointer to the frame rate and feels laggy. The viewer shows its
	// own (instant) local cursor instead; drawCursor remains for a future
	// separate high-rate cursor channel.
	// DIB is BGRX top-down; convert to RGBA in place.
	src, dst := c.pixels, c.img.Pix
	for i := 0; i+3 < len(src); i += 4 {
		dst[i] = src[i+2]   // R
		dst[i+1] = src[i+1] // G
		dst[i+2] = src[i]   // B
		dst[i+3] = 255      // A
	}
	return c.img, nil
}

// ---- input injection ----

func sendMouse(flags uint32, x, y float64, data int32) {
	in := input{typ: inputMouse}
	mi := (*mouseInput)(unsafe.Pointer(&in.u[0]))
	if flags&mvAbsolute != 0 {
		// Absolute coords are normalized 0..65535 across the whole virtual
		// desktop (matches the stitched multi-monitor frame we stream).
		flags |= mvVirtualDesk
		mi.dx = int32(x * 65535)
		mi.dy = int32(y * 65535)
	}
	mi.dwFlags = flags
	mi.mouseData = uint32(data)
	procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

func sendKey(code uint16, down bool) {
	in := input{typ: inputKeyboard}
	ki := (*keybdInput)(unsafe.Pointer(&in.u[0]))
	ki.wVk = code
	if !down {
		ki.dwFlags = keyUp
	}
	procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

type inputEvent struct {
	T      string  `json:"t"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Button int     `json:"button"`
	Down   bool    `json:"down"`
	DY     float64 `json:"dy"`
	Code   int     `json:"code"`
}

func handleInput(data []byte) {
	var e inputEvent
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch e.T {
	case "m":
		sendMouse(mvMove|mvAbsolute, e.X, e.Y, 0)
	case "b":
		var f uint32 = mvMove | mvAbsolute
		switch e.Button {
		case 0:
			f |= map[bool]uint32{true: mvLeftDown, false: mvLeftUp}[e.Down]
		case 1:
			f |= map[bool]uint32{true: mvMidDown, false: mvMidUp}[e.Down]
		case 2:
			f |= map[bool]uint32{true: mvRightDown, false: mvRightUp}[e.Down]
		}
		sendMouse(f, e.X, e.Y, 0)
	case "w":
		sendMouse(mvWheel, 0, 0, int32(-e.DY))
	case "k":
		sendKey(uint16(e.Code), e.Down)
	}
}

// ---- session loop ----

type wsMsg struct {
	mt   int
	data []byte
}

// session multiplexes video, input, chat and file transfer over one relay conn.
// A single writer goroutine serializes all sends (gorilla forbids concurrent
// writes).
type session struct {
	conn       *websocket.Conn
	out        chan wsMsg
	done       chan struct{}
	inputCh    chan []byte // input events handed to the desktop worker
	chat       *chatWindow
	mu         sync.Mutex
	uploads    map[uint8]*os.File // active viewer->agent uploads, by transfer id
	uacOff     bool               // true while we've moved UAC prompts off the secure desktop
	kfReq      atomic.Bool        // a viewer asked for a full keyframe (e.g. on reconnect)
	lockWanted atomic.Bool        // a viewer asked to block the host's physical input
	allowFiles bool
}

func (s *session) send(mt int, data []byte) {
	select {
	case s.out <- wsMsg{mt, data}:
	case <-s.done:
	}
}

func (s *session) sendJSON(v any) {
	b, _ := json.Marshal(v)
	s.send(websocket.TextMessage, b)
}

func runMedia(conn *websocket.Conn, allowFiles bool) error {
	s := &session{conn: conn, out: make(chan wsMsg, 32), done: make(chan struct{}),
		inputCh: make(chan []byte, 256), uploads: map[uint8]*os.File{}, allowFiles: allowFiles}
	defer close(s.done)
	s.chat = newChatWindow("OpenRMM — Chat with technician", func(text string) {
		s.sendJSON(map[string]any{"t": "chat", "from": "user", "text": text})
	})
	defer func() {
		if s.chat != nil {
			s.chat.close()
		}
		if s.uacOff {
			s.setUAC(false) // restore the secure desktop on disconnect
		}
	}()

	errc := make(chan error, 3)
	// single writer
	go func() {
		for {
			select {
			case <-s.done:
				return
			case m := <-s.out:
				if err := conn.WriteMessage(m.mt, m.data); err != nil {
					errc <- err
					return
				}
			}
		}
	}()
	// announce the running sidecar version so the viewer can surface staleness
	s.sendJSON(map[string]any{"t": "hello", "v": remoteVersion, "allow_files": allowFiles})
	// Auto-enable UAC control so elevation prompts are visible/clickable without
	// a manual click; restored to the secure default on disconnect. The viewer
	// toggle can still turn it back off mid-session.
	go s.setUAC(true)
	// reader / dispatcher
	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			s.onMessage(mt, data)
		}
	}()
	// Input lock follows the active input desktop on its own thread (idle until a
	// viewer enables it); released automatically when the session ends.
	go s.inputLockWorker()
	// Capture + input on a dedicated thread that FOLLOWS the active input desktop
	// (default / secure-UAC / Winlogon login / lock). Running as SYSTEM, this lets
	// us stream the logon screen and inject into elevated/secure desktops, and
	// survive desktop switches.
	go s.desktopWorker()
	return <-errc
}

const genericAll = 0x10000000

// desktopWorker streams frames and injects input, re-attaching its thread to
// whichever desktop is currently receiving input.
func (s *session) desktopWorker() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var cap *capturer
	var curDesk uintptr
	var curName string
	enc := newVideoEncoder(jpegQuality())
	pace := newPacer()
	defer func() {
		if cap != nil {
			cap.close()
		}
		if curDesk != 0 {
			procCloseDesktop.Call(curDesk)
		}
	}()

	// ensure the thread is attached to the active input desktop, rebuilding the
	// capturer when the desktop (or resolution) changes or a BitBlt fails.
	ensure := func(force bool) {
		h, _, _ := procOpenInputDesktop.Call(0, 0, genericAll)
		if h == 0 {
			return
		}
		name := desktopName(h)
		if !force && name != "" && name == curName && cap != nil {
			procCloseDesktop.Call(h)
			return
		}
		if r, _, _ := procSetThreadDesktop.Call(h); r == 0 {
			procCloseDesktop.Call(h)
			return
		}
		if cap != nil {
			cap.close()
			cap = nil
		}
		if nc, e := newCapturer(); e == nil {
			cap = nc
		}
		enc.reset() // new desktop/resolution → next frame is a full keyframe
		if curDesk != 0 {
			procCloseDesktop.Call(curDesk)
		}
		curDesk, curName = h, name
	}

	ticker := time.NewTicker(pace.active)
	defer ticker.Stop()
	curInterval := pace.active
	for {
		select {
		case <-s.done:
			return
		case data := <-s.inputCh:
			pace.markActivity()
			ensure(false)
			handleInput(data)
		case <-ticker.C:
			// Backpressure: skip the tick before capture/encode when the writer
			// hasn't drained the queue — the tile diff folds the change into the
			// next frame, so skipping costs nothing but latency (see tunables.go).
			if len(s.out) >= videoBacklog {
				continue
			}
			ensure(false)
			if cap == nil {
				continue
			}
			if s.kfReq.Swap(false) {
				pace.markActivity() // viewer (re)connected — resume the active rate
				enc.forceKeyframe()
			}
			img, err := cap.frame()
			if err != nil {
				ensure(true) // rebuild (desktop likely switched)
				continue
			}
			payload, err := enc.encode(img)
			sent := false
			if err == nil && payload != nil {
				select {
				case s.out <- wsMsg{websocket.BinaryMessage, append([]byte{chVideo}, payload...)}:
					sent = true
				case <-s.done:
					return
				}
			}
			// Adaptive cadence: idle screens capture at the idle rate; a sent
			// frame or input restores the active rate.
			if iv := pace.interval(sent); iv != curInterval {
				curInterval = iv
				ticker.Reset(iv)
			}
		}
	}
}

// desktopName returns the name of a desktop handle (e.g. "Default", "Winlogon").
func desktopName(h uintptr) string {
	var buf [256]uint16
	var needed uint32
	const uoiName = 2
	procGetUserObjectInfo.Call(h, uoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&needed)))
	return syscall.UTF16ToString(buf[:])
}

// onMessage routes a relay frame to input / chat / file handling.
func (s *session) onMessage(mt int, data []byte) {
	if mt == websocket.BinaryMessage {
		if s.allowFiles && len(data) >= 2 && data[0] == chFile {
			s.onUploadChunk(data[1], data[2:])
		}
		return
	}
	var e struct {
		T      string `json:"t"`
		Text   string `json:"text"`
		ID     uint8  `json:"id"`
		Enable bool   `json:"enable"`
	}
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch e.T {
	case "m", "b", "w", "k":
		// hand to the desktop worker so injection runs on the active desktop;
		// drop if backed up (mouse-move spam) rather than block the reader.
		select {
		case s.inputCh <- data:
		default:
		}
	case "kf":
		// Viewer (re)connected or its canvas was reset — force a full frame.
		s.kfReq.Store(true)
	case "chat":
		if s.chat != nil {
			s.chat.append("Tech: " + e.Text)
		}
	case "uac":
		go s.setUAC(e.Enable)
	case "lockinput":
		go s.setInputLock(e.Enable)
	case "fls":
		if s.allowFiles {
			go s.onList(data)
		}
	case "fmkdir":
		if s.allowFiles {
			go s.onMkdir(data)
		}
	case "fdel":
		if s.allowFiles {
			go s.onDelete(data)
		}
	case "frename":
		if s.allowFiles {
			go s.onRename(data)
		}
	case "fcopy":
		if s.allowFiles {
			go s.onCopy(data)
		}
	case "fmove":
		if s.allowFiles {
			go s.onMove(data)
		}
	case "fput":
		if s.allowFiles {
			s.onUploadStart(data)
		}
	case "fget":
		if s.allowFiles {
			go s.onDownload(data)
		}
	case "fend":
		if s.allowFiles {
			s.onUploadEnd(e.ID)
		}
	}
}
