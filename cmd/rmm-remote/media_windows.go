//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"os"
	"runtime"
	"sort"
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
	procEnumDisplayMons   = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfo    = user32.NewProc("GetMonitorInfoW")
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

// ---- monitors ----

// monitorInfo is one physical display, in virtual-desktop coordinates.
type monitorInfo struct {
	X, Y, W, H int32
	Primary    bool
	Name       string
}

type monitorInfoEx struct {
	cbSize    uint32
	rcMonitor winRect
	rcWork    winRect
	dwFlags   uint32
	szDevice  [32]uint16
}

type winRect struct{ Left, Top, Right, Bottom int32 }

// The callback is created once: syscall.NewCallback allocates from a small
// process-wide pool that is never reclaimed, and monitors are re-enumerated on
// every desktop switch.
var (
	monitorMu   sync.Mutex
	monitorList []monitorInfo
	monitorCB   = syscall.NewCallback(monitorEnumProc)
)

func monitorEnumProc(h, _, _, _ uintptr) uintptr {
	mi := monitorInfoEx{}
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfo.Call(h, uintptr(unsafe.Pointer(&mi))); r != 0 {
		monitorList = append(monitorList, monitorInfo{
			X: mi.rcMonitor.Left, Y: mi.rcMonitor.Top,
			W:       mi.rcMonitor.Right - mi.rcMonitor.Left,
			H:       mi.rcMonitor.Bottom - mi.rcMonitor.Top,
			Primary: mi.dwFlags&1 != 0, // MONITORINFOF_PRIMARY
			Name:    syscall.UTF16ToString(mi.szDevice[:]),
		})
	}
	return 1 // keep enumerating
}

// listMonitors returns the displays ordered left to right, then top to bottom,
// so that selection index N means the same display to the viewer and to the
// capturer even though Windows enumerates in an arbitrary order.
func listMonitors() []monitorInfo {
	monitorMu.Lock()
	defer monitorMu.Unlock()
	monitorList = nil
	procEnumDisplayMons.Call(0, 0, monitorCB, 0)
	out := append([]monitorInfo(nil), monitorList...)
	monitorList = nil
	sort.Slice(out, func(i, j int) bool {
		if out[i].X != out[j].X {
			return out[i].X < out[j].X
		}
		return out[i].Y < out[j].Y
	})
	return out
}

// describeMonitors is the hello payload: one entry per display, in the same
// order the capturer indexes them (selection 1..N), so the viewer's choice and
// the captured region can never disagree.
func describeMonitors() []map[string]any {
	mons := listMonitors()
	if len(mons) < 2 {
		return nil // a single display needs no selector
	}
	out := make([]map[string]any, 0, len(mons))
	for i, m := range mons {
		out = append(out, map[string]any{
			"id": i + 1, "w": m.W, "h": m.H, "primary": m.Primary, "name": m.Name,
		})
	}
	return out
}

// ---- screen capture ----

type capturer struct {
	w, h   int
	vx, vy int32 // captured region origin, in virtual-desktop coordinates
	dx, dy int32 // virtual-desktop origin (top-left can be negative)
	dw, dh int32 // virtual-desktop size — SendInput's absolute space
	screen uintptr
	mem    uintptr
	bitmap uintptr
	pixels []byte
	img    *image.RGBA
}

// toVirtual maps a viewer coordinate — normalized against the captured region —
// onto the whole virtual desktop, which is the space MOUSEEVENTF_VIRTUALDESK
// uses. Capturing one monitor changes what 0..1 means; injection must not move
// with it, or clicks land on the wrong display.
func (c *capturer) toVirtual(x, y float64) (float64, float64) {
	if c == nil || c.dw <= 0 || c.dh <= 0 {
		return x, y
	}
	vx := (float64(c.vx-c.dx) + x*float64(c.w)) / float64(c.dw)
	vy := (float64(c.vy-c.dy) + y*float64(c.h)) / float64(c.dh)
	return vx, vy
}

// newCapturer captures the whole virtual desktop (all monitors stitched into one
// image) when sel is 0, or a single display when sel is 1..N. An out-of-range
// selection falls back to the whole desktop rather than failing the session: the
// display may have been unplugged since the viewer listed it.
func newCapturer(sel int32) (*capturer, error) {
	procSetDPIAware.Call()
	rdx, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	rdy, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	rdw, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	rdh, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	dx, dy, dw, dh := int32(rdx), int32(rdy), int32(rdw), int32(rdh)
	vx, vy, w, h := dx, dy, dw, dh
	if sel > 0 {
		if mons := listMonitors(); int(sel) <= len(mons) {
			m := mons[sel-1]
			vx, vy, w, h = m.X, m.Y, m.W, m.H
		}
	}
	if w <= 0 || h <= 0 {
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
		w: int(w), h: int(h), vx: vx, vy: vy,
		dx: dx, dy: dy, dw: dw, dh: dh,
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

// handleInput injects one viewer event. Pointer coordinates arrive normalized
// against whatever region is being captured, so they go through the capturer
// before reaching SendInput's virtual-desktop space.
func handleInput(data []byte, c *capturer) {
	var e inputEvent
	if json.Unmarshal(data, &e) != nil {
		return
	}
	x, y := c.toVirtual(e.X, e.Y)
	switch e.T {
	case "m":
		sendMouse(mvMove|mvAbsolute, x, y, 0)
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
		sendMouse(f, x, y, 0)
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
	monitor    atomic.Int32       // 0 = whole virtual desktop, 1..N = one display
	monReq     atomic.Bool        // the selection changed; rebuild the capturer
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
	// Announce optional capabilities as well as the version. Ctrl+Alt+Delete is
	// a Windows Secure Attention Sequence, not an injectable keyboard chord, so
	// the viewer only enables its button when SendSAS and the local policy allow it.
	hello := map[string]any{"t": "hello", "v": remoteVersion, "allow_files": allowFiles, "chat": s.chat != nil && s.chat.hwnd != 0}
	if err := secureAttentionAvailable(); err != nil {
		// Announced once per session and never revised, so when the button comes
		// up greyed this line is the only record of why.
		log.Printf("cad: secure attention unavailable: %v", err)
		hello["cad"] = false
		hello["cad_error"] = err.Error()
	} else {
		hello["cad"] = true
	}
	s.sendJSON(hello)
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
		if nc, e := newCapturer(s.monitor.Load()); e == nil {
			cap = nc
		}
		enc.reset() // new desktop/resolution → next frame is a full keyframe
		s.sendJSON(map[string]any{"t": "monitors", "list": describeMonitors(), "selected": s.monitor.Load()})
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
			handleInput(data, cap)
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
			if s.monReq.Swap(false) {
				ensure(true) // different region: rebuild the DIB and re-keyframe
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
	// File-transfer-only sessions must never invoke desktop controls.
	if s.inputCh == nil && (e.T == "cad" || e.T == "uac" || e.T == "lockinput" || e.T == "chat" || e.T == "mon") {
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
	case "mon":
		// Clamp here: the desktop worker falls back to the whole desktop for an
		// unknown index, but storing it would make the viewer and host disagree.
		if e.ID <= uint8(len(listMonitors())) {
			s.monitor.Store(int32(e.ID))
			s.monReq.Store(true)
		}
	case "cad":
		// The named-pipe round-trip can wait on the broker; don't stall input.
		go func() {
			if err := sendSecureAttention(); err != nil {
				s.sendJSON(map[string]any{"t": "cad", "ok": false, "err": err.Error()})
			} else {
				s.sendJSON(map[string]any{"t": "cad", "ok": true})
			}
		}()
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
