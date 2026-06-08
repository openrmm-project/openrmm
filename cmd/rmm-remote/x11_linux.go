//go:build linux

package main

// Minimal pure-Go X11 client (no CGO, no deps) — the "same technique" as the
// Windows GDI engine, applied to X11: connect to the display, GetImage the root
// window each frame, and inject input via the XTEST extension. The captured
// RGBA frames feed the shared tile-delta codec (encoder.go); no fork.
//
// Scope: TrueColor 24/32-bit visuals on a little-endian server (the overwhelming
// common case). Multi-monitor is captured as the first screen's full size
// (RandR stitches monitors into one screen). Keyboard is best-effort via a
// browser-keyCode→keysym table resolved through the server keymap.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// X11 request opcodes / constants we use.
const (
	opQueryExtension     = 98
	opGetImage           = 73
	opGetKeyboardMapping = 101
	zPixmap              = 2

	evKeyPress     = 2
	evKeyRelease   = 3
	evButtonPress  = 4
	evButtonRelease = 5
	evMotionNotify = 6
)

var le = binary.LittleEndian

type x11 struct {
	conn               net.Conn
	r                  *bufio.Reader
	root               uint32
	width, height      int
	minKey, maxKey     int
	xtestOp            byte // XTEST major opcode (0 = unavailable)
	keycodeFor         map[uint32]byte
	img                *image.RGBA
	raw                []byte // reusable GetImage payload buffer
}

// dialX11 opens the display socket and returns the connection + display number.
func dialX11() (net.Conn, int, error) {
	disp := os.Getenv("DISPLAY")
	if disp == "" {
		disp = ":0"
	}
	host, rest := "", disp
	if i := strings.LastIndex(disp, ":"); i >= 0 {
		host, rest = disp[:i], disp[i+1:]
	}
	num := rest
	if j := strings.IndexByte(rest, '.'); j >= 0 {
		num = rest[:j]
	}
	n, _ := strconv.Atoi(num)
	if host == "" || host == "unix" {
		// Abstract socket first (modern Linux), then the filesystem path.
		if c, err := net.Dial("unix", "@/tmp/.X11-unix/X"+strconv.Itoa(n)); err == nil {
			return c, n, nil
		}
		c, err := net.Dial("unix", "/tmp/.X11-unix/X"+strconv.Itoa(n))
		return c, n, err
	}
	c, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(6000+n)))
	return c, n, err
}

// xauth reads the MIT-MAGIC-COOKIE-1 for the display from ~/.Xauthority.
func xauth(displayNum int) (name, data []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	want := strconv.Itoa(displayNum)
	off := 0
	blk := func() ([]byte, bool) {
		if off+2 > len(b) {
			return nil, false
		}
		l := int(binary.BigEndian.Uint16(b[off:]))
		off += 2
		if off+l > len(b) {
			return nil, false
		}
		s := b[off : off+l]
		off += l
		return s, true
	}
	for off+2 <= len(b) {
		off += 2 // family
		if _, ok := blk(); !ok {
			break
		} // address
		num, ok := blk()
		if !ok {
			break
		}
		nm, ok := blk()
		if !ok {
			break
		}
		dt, ok := blk()
		if !ok {
			break
		}
		if string(nm) == "MIT-MAGIC-COOKIE-1" && (len(num) == 0 || string(num) == want) {
			return nm, dt
		}
	}
	return nil, nil
}

func pad4(n int) int { return (4 - n%4) % 4 }

// newX11 connects, authenticates, and parses the server setup (root window,
// screen size, keycode range), then resolves XTEST + the keymap.
func newX11() (*x11, error) {
	conn, dnum, err := dialX11()
	if err != nil {
		return nil, err
	}
	name, data := xauth(dnum)

	// Connection setup request.
	req := make([]byte, 0, 64)
	req = append(req, 'l', 0) // little-endian
	req = le.AppendUint16(req, 11)
	req = le.AppendUint16(req, 0)
	req = le.AppendUint16(req, uint16(len(name)))
	req = le.AppendUint16(req, uint16(len(data)))
	req = le.AppendUint16(req, 0)
	req = append(req, name...)
	req = append(req, make([]byte, pad4(len(name)))...)
	req = append(req, data...)
	req = append(req, make([]byte, pad4(len(data)))...)
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	r := bufio.NewReaderSize(conn, 1<<16)
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(r, hdr); err != nil {
		conn.Close()
		return nil, err
	}
	extra := make([]byte, int(le.Uint16(hdr[6:8]))*4)
	if _, err := io.ReadFull(r, extra); err != nil {
		conn.Close()
		return nil, err
	}
	switch hdr[0] {
	case 0:
		reason := ""
		if int(hdr[1]) <= len(extra) {
			reason = string(extra[:hdr[1]])
		}
		conn.Close()
		return nil, fmt.Errorf("x11 setup refused: %s", reason)
	case 2:
		conn.Close()
		return nil, fmt.Errorf("x11 setup requires further authentication")
	}

	x := &x11{conn: conn, r: r, keycodeFor: map[uint32]byte{}}
	if err := x.parseSetup(extra); err != nil {
		conn.Close()
		return nil, err
	}
	x.img = image.NewRGBA(image.Rect(0, 0, x.width, x.height))
	x.queryXTest()
	x.loadKeymap()
	return x, nil
}

// parseSetup extracts the first screen's root window + size and the keycode
// range from the setup reply's additional data.
func (x *x11) parseSetup(b []byte) error {
	if len(b) < 40 {
		return fmt.Errorf("x11 setup too short")
	}
	p := 0
	u32 := func() uint32 { v := le.Uint32(b[p:]); p += 4; return v }
	u16 := func() uint16 { v := le.Uint16(b[p:]); p += 2; return v }
	u8 := func() byte { v := b[p]; p++; return v }
	_ = u32() // release number
	_ = u32() // resource-id base
	_ = u32() // resource-id mask
	_ = u32() // motion buffer size
	vlen := int(u16())
	_ = u16() // max request length
	_ = u8()  // number of screens
	nfmt := int(u8())
	_ = u8() // image byte order (assume LSBFirst)
	_ = u8() // bitmap bit order
	_ = u8() // scanline unit
	_ = u8() // scanline pad
	x.minKey = int(u8())
	x.maxKey = int(u8())
	p += 4 // unused
	p += vlen + pad4(vlen)
	p += 8 * nfmt
	if p+40 > len(b) {
		return fmt.Errorf("x11 setup: screen out of range")
	}
	x.root = u32()
	_ = u32() // default colormap
	_ = u32() // white pixel
	_ = u32() // black pixel
	_ = u32() // current input masks
	x.width = int(u16())
	x.height = int(u16())
	if x.width == 0 || x.height == 0 {
		return fmt.Errorf("x11 reported zero screen size")
	}
	return nil
}

func (x *x11) close() {
	if x.conn != nil {
		x.conn.Close()
	}
}

// readReply returns the next 32-byte reply header, discarding any X errors or
// events that arrive first (we never select events, but FakeInput may error).
func (x *x11) readReply() ([]byte, error) {
	h := make([]byte, 32)
	for {
		if _, err := io.ReadFull(x.r, h); err != nil {
			return nil, err
		}
		if h[0] == 1 { // reply
			return h, nil
		}
		// h[0]==0 error, >1 event: 32 bytes, already consumed; keep reading.
	}
}

// getImage captures the whole root window into x.img and returns it.
func (x *x11) getImage() (*image.RGBA, error) {
	req := make([]byte, 20)
	req[0] = opGetImage
	req[1] = zPixmap
	le.PutUint16(req[2:], 5) // request length in 4-byte units
	le.PutUint32(req[4:], x.root)
	// x, y = 0 (int16); width, height
	le.PutUint16(req[12:], uint16(x.width))
	le.PutUint16(req[14:], uint16(x.height))
	le.PutUint32(req[16:], 0xffffffff) // plane mask
	if _, err := x.conn.Write(req); err != nil {
		return nil, err
	}
	h, err := x.readReply()
	if err != nil {
		return nil, err
	}
	n := int(le.Uint32(h[4:8])) * 4 // image bytes follow
	if cap(x.raw) < n {
		x.raw = make([]byte, n)
	}
	x.raw = x.raw[:n]
	if _, err := io.ReadFull(x.r, x.raw); err != nil {
		return nil, err
	}
	// ZPixmap depth-24/32 on LSBFirst is BGRX per pixel → convert to RGBA.
	src, dst := x.raw, x.img.Pix
	px := x.width * x.height * 4
	if len(src) < px {
		px = len(src)
	}
	for i := 0; i+3 < px && i+3 < len(dst); i += 4 {
		dst[i] = src[i+2]   // R
		dst[i+1] = src[i+1] // G
		dst[i+2] = src[i]   // B
		dst[i+3] = 255      // A
	}
	return x.img, nil
}

// queryXTest resolves the XTEST major opcode (0 if the extension is absent).
func (x *x11) queryXTest() {
	const ext = "XTEST"
	nlen := len(ext)
	body := 4 + nlen + pad4(nlen)
	req := make([]byte, 4+body)
	req[0] = opQueryExtension
	le.PutUint16(req[2:], uint16(len(req)/4))
	le.PutUint16(req[4:], uint16(nlen))
	copy(req[8:], ext)
	if _, err := x.conn.Write(req); err != nil {
		return
	}
	h, err := x.readReply()
	if err != nil {
		return
	}
	if h[8] != 0 { // present
		x.xtestOp = h[9] // major opcode
	}
}

// loadKeymap fetches the server keyboard mapping and builds keysym→keycode.
func (x *x11) loadKeymap() {
	if x.minKey == 0 || x.maxKey < x.minKey {
		return
	}
	count := x.maxKey - x.minKey + 1
	req := make([]byte, 8)
	req[0] = opGetKeyboardMapping
	le.PutUint16(req[2:], 2)
	req[4] = byte(x.minKey)
	req[5] = byte(count)
	if _, err := x.conn.Write(req); err != nil {
		return
	}
	h, err := x.readReply()
	if err != nil {
		return
	}
	perKey := int(h[1])
	n := int(le.Uint32(h[4:8])) * 4
	data := make([]byte, n)
	if _, err := io.ReadFull(x.r, data); err != nil {
		return
	}
	if perKey == 0 {
		return
	}
	for kc := 0; kc < count; kc++ {
		for col := 0; col < perKey; col++ {
			off := (kc*perKey + col) * 4
			if off+4 > len(data) {
				break
			}
			ks := le.Uint32(data[off:])
			if ks == 0 {
				continue
			}
			if _, ok := x.keycodeFor[ks]; !ok { // column 0 (unshifted) wins
				x.keycodeFor[ks] = byte(x.minKey + kc)
			}
		}
	}
}

// fakeInput sends an XTestFakeInput request (40-byte modern form, length=10).
func (x *x11) fakeInput(typ, detail byte, rootX, rootY int) {
	if x.xtestOp == 0 {
		return
	}
	req := make([]byte, 40)
	req[0] = x.xtestOp
	req[1] = 2 // X_XTestFakeInput
	le.PutUint16(req[2:], 10)
	req[4] = typ
	req[5] = detail
	// time(8..11)=0 CurrentTime; root(12..15)
	le.PutUint32(req[12:], x.root)
	// rootX/rootY at offsets 24/26 (after 8 bytes of pad1)
	le.PutUint16(req[24:], uint16(int16(rootX)))
	le.PutUint16(req[26:], uint16(int16(rootY)))
	x.conn.Write(req)
}

func (x *x11) fakeMotion(px, py int)        { x.fakeInput(evMotionNotify, 0, px, py) }
func (x *x11) fakeButton(button byte, down bool) {
	x.fakeInput(btnEvent(down), button, 0, 0)
}
func (x *x11) fakeKey(keycode byte, down bool) {
	x.fakeInput(keyEvent(down), keycode, 0, 0)
}

func btnEvent(down bool) byte {
	if down {
		return evButtonPress
	}
	return evButtonRelease
}
func keyEvent(down bool) byte {
	if down {
		return evKeyPress
	}
	return evKeyRelease
}

// keycodeForBrowser maps a browser keyCode to an X keycode via the keysym table.
func (x *x11) keycodeForBrowser(code int) (byte, bool) {
	ks, ok := browserKeysym[code]
	if !ok {
		return 0, false
	}
	kc, ok := x.keycodeFor[ks]
	return kc, ok
}

// browserKeysym maps DOM keyCode values to X11 keysyms (US layout, best effort).
var browserKeysym = map[int]uint32{
	8: 0xff08, 9: 0xff09, 13: 0xff0d, 16: 0xffe1, 17: 0xffe3, 18: 0xffe9,
	19: 0xff13, 20: 0xffe5, 27: 0xff1b, 32: 0x20, 33: 0xff55, 34: 0xff56,
	35: 0xff57, 36: 0xff50, 37: 0xff51, 38: 0xff52, 39: 0xff53, 40: 0xff54,
	45: 0xff63, 46: 0xffff, 91: 0xffeb, 93: 0xff67,
	186: 0x3b, 187: 0x3d, 188: 0x2c, 189: 0x2d, 190: 0x2e, 191: 0x2f,
	192: 0x60, 219: 0x5b, 220: 0x5c, 221: 0x5d, 222: 0x27,
}

func init() {
	for i := 0; i <= 9; i++ { // digits 0-9
		browserKeysym[48+i] = uint32(0x30 + i)
		browserKeysym[96+i] = uint32(0x30 + i) // numpad
	}
	for i := 0; i < 26; i++ { // A-Z → lowercase a-z keysym
		browserKeysym[65+i] = uint32(0x61 + i)
	}
	for i := 0; i < 12; i++ { // F1-F12
		browserKeysym[112+i] = uint32(0xffbe + i)
	}
}
