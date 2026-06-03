//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"syscall"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
)

// ---- Win32 bindings (CGO-free, via lazy DLLs) ----

var (
	user32             = syscall.NewLazyDLL("user32.dll")
	gdi32              = syscall.NewLazyDLL("gdi32.dll")
	procGetDC            = user32.NewProc("GetDC")
	procReleaseDC        = user32.NewProc("ReleaseDC")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procSendInput        = user32.NewProc("SendInput")
	procSetDPIAware      = user32.NewProc("SetProcessDPIAware")
	procGetCursorInfo    = user32.NewProc("GetCursorInfo")
	procGetIconInfo      = user32.NewProc("GetIconInfo")
	procDrawIconEx       = user32.NewProc("DrawIconEx")
	procCreateDC         = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIB        = gdi32.NewProc("CreateDIBSection")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procBitBlt           = gdi32.NewProc("BitBlt")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procDeleteDC         = gdi32.NewProc("DeleteDC")
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

	mvMove      = 0x0001
	mvLeftDown  = 0x0002
	mvLeftUp    = 0x0004
	mvRightDown = 0x0008
	mvRightUp   = 0x0010
	mvMidDown   = 0x0020
	mvMidUp     = 0x0040
	mvWheel     = 0x0800
	mvAbsolute  = 0x8000
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

// frameJPEG grabs the screen and returns it JPEG-encoded.
func (c *capturer) frameJPEG(buf *bytes.Buffer) ([]byte, error) {
	r, _, _ := procBitBlt.Call(c.mem, 0, 0, uintptr(c.w), uintptr(c.h), c.screen, uintptr(c.vx), uintptr(c.vy), srcCopy|captureBlt)
	if r == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}
	c.drawCursor()
	// DIB is BGRX top-down; convert to RGBA.
	src, dst := c.pixels, c.img.Pix
	for i := 0; i+3 < len(src); i += 4 {
		dst[i] = src[i+2]   // R
		dst[i+1] = src[i+1] // G
		dst[i+2] = src[i]   // B
		dst[i+3] = 255      // A
	}
	buf.Reset()
	if err := jpeg.Encode(buf, c.img, &jpeg.Options{Quality: 55}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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

func runMedia(conn *websocket.Conn) error {
	cap, err := newCapturer()
	if err != nil {
		return err
	}
	defer cap.close()

	errc := make(chan error, 2)
	// viewer -> agent: input events
	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if mt == websocket.TextMessage {
				handleInput(data)
			}
		}
	}()
	// agent -> viewer: screen frames (~12 fps)
	go func() {
		var buf bytes.Buffer
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			jpg, err := cap.frameJPEG(&buf)
			if err != nil {
				errc <- err
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, jpg); err != nil {
				errc <- err
				return
			}
		}
	}()
	return <-errc
}
