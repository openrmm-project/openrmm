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
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// X11 request opcodes / constants we use.
const (
	opQueryExtension     = 98
	opGetInputFocus      = 43 // a tiny request with a reply — used to sync (round-trip)
	opGetImage           = 73
	opGetKeyboardMapping = 101
	zPixmap              = 2

	evKeyPress      = 2
	evKeyRelease    = 3
	evButtonPress   = 4
	evButtonRelease = 5
	evMotionNotify  = 6

	// MIT-SHM minor opcodes.
	shmQueryVersion = 0
	shmAttach       = 1
	shmDetach       = 2
	shmGetImage     = 4
)

// After a SHM failure, (re)attaches skip SHM for a cooldown window instead of
// forever: retrying immediately would loop on a genuinely broken path (the
// reason this guard exists), but a single transient failure — e.g. mid
// session-switch — shouldn't condemn the rest of a long session to the slow
// socket GetImage either.
var shmFailedAt atomic.Int64 // UnixNano of the last SHM failure (0 = never)

const shmRetryAfter = 5 * time.Minute

var le = binary.LittleEndian

type x11 struct {
	conn             net.Conn
	r                *bufio.Reader
	root             uint32
	width, height    int  // captured region size (the whole screen unless a monitor is picked)
	originX, originY int  // captured region origin on the root window
	screenW, screenH int  // whole X screen — RandR has already stitched the monitors
	randrOp          byte // RandR major opcode (0 = absent or older than 1.5)
	minKey, maxKey   int
	xtestOp          byte // XTEST major opcode (0 = unavailable)
	keycodeFor       map[uint32]byte
	img              *image.RGBA
	raw              []byte // reusable socket GetImage payload buffer

	xidBase, xidMask uint32 // resource-id allocation (for the SHM segment XID)
	xidNext          uint32

	// MIT-SHM: when available the server writes GetImage pixels into a shared
	// memory segment instead of marshaling ~W*H*4 bytes over the socket each
	// frame — the main cost on Linux. Falls back to the socket path on any error.
	shmOK   bool
	shmOp   byte   // MIT-SHM major opcode
	shmSeg  uint32 // X resource id for the attached segment
	shmID   int    // System V shm id
	shmAddr []byte // the segment, mapped into our address space
}

// dialX11 opens the display socket from $DISPLAY (fallback :0).
func dialX11() (net.Conn, int, error) {
	disp := os.Getenv("DISPLAY")
	if disp == "" {
		disp = ":0"
	}
	return dialDisplay(disp)
}

// dialDisplay opens the socket for an explicit display string (e.g. ":0").
func dialDisplay(disp string) (net.Conn, int, error) {
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

// xauth reads the MIT-MAGIC-COOKIE-1 for the display from $XAUTHORITY/~/.Xauthority.
func xauth(displayNum int) (name, data []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	return authFromFile(path, displayNum)
}

// authFromFile reads the MIT-MAGIC-COOKIE-1 for a display from a specific
// Xauthority file (used by seat-following to read the greeter's cookie).
func authFromFile(path string, displayNum int) (name, data []byte) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	return parseXauth(b, strconv.Itoa(displayNum))
}

// parseXauth scans an Xauthority blob for the MIT-MAGIC-COOKIE-1 entry matching
// the wanted display number (empty number in an entry matches any display).
func parseXauth(b []byte, want string) (name, data []byte) {
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

// newX11 connects using $DISPLAY and the environment/home Xauthority.
func newX11() (*x11, error) {
	conn, dnum, err := dialX11()
	if err != nil {
		return nil, err
	}
	name, data := xauth(dnum)
	return connectX11(conn, name, data)
}

// newX11At connects to an explicit display using a specific Xauthority file —
// used by seat-following to attach to the active session or the DM greeter.
func newX11At(display, authPath string) (*x11, error) {
	conn, dnum, err := dialDisplay(display)
	if err != nil {
		return nil, err
	}
	name, data := authFromFile(authPath, dnum)
	return connectX11(conn, name, data)
}

// connectX11 performs the X11 setup handshake on an open connection and parses
// the server reply (root window, screen size, keycode range), then resolves
// XTEST + the keymap.
func connectX11(conn net.Conn, name, data []byte) (*x11, error) {
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
	x.queryRandR()
	x.loadKeymap()
	x.setupShm()
	return x, nil
}

// newXID allocates an X resource id (for the SHM segment).
func (x *x11) newXID() uint32 {
	id := x.xidBase | (x.xidNext & x.xidMask)
	x.xidNext++
	return id
}

// setupShm negotiates MIT-SHM and attaches a shared segment so the server can
// deliver GetImage pixels via shared memory. Local connections only; any failure
// leaves shmOK false (and the socket path is used). Synchronous: all replies are
// immediate (no hang) — ShmAttach has no reply, so a GetInputFocus round-trip
// confirms the server processed it before we mark the segment for cleanup.
func (x *x11) setupShm() {
	if t := shmFailedAt.Load(); t != 0 && time.Since(time.Unix(0, t)) < shmRetryAfter {
		return // recent SHM failure — stay on the socket path for the cooldown
	}
	if _, ok := x.conn.(*net.UnixConn); !ok {
		return // SHM only works when the server is on the same machine
	}
	op, present := x.queryExtensionMajor("MIT-SHM")
	if !present {
		log.Printf("rmm-remote: MIT-SHM absent — using socket GetImage")
		return
	}
	// ShmQueryVersion: confirm the extension is usable.
	qv := []byte{op, shmQueryVersion, 1, 0}
	if _, err := x.conn.Write(qv); err != nil {
		return
	}
	if _, err := x.readReply(); err != nil {
		return
	}

	size := x.width * x.height * 4
	id, err := unix.SysvShmGet(unix.IPC_PRIVATE, size, unix.IPC_CREAT|0o600)
	if err != nil {
		log.Printf("rmm-remote: shmget failed (%v) — using socket GetImage", err)
		return
	}
	addr, err := unix.SysvShmAttach(id, 0, 0)
	if err != nil {
		_, _ = unix.SysvShmCtl(id, unix.IPC_RMID, nil)
		return
	}
	seg := x.newXID()
	// ShmAttach(shmseg, shmid, readOnly=false) — server must be able to write.
	at := make([]byte, 16)
	at[0] = op
	at[1] = shmAttach
	le.PutUint16(at[2:], 4)
	le.PutUint32(at[4:], seg)
	le.PutUint32(at[8:], uint32(id))
	// at[12] readOnly = 0
	if _, err := x.conn.Write(at); err != nil {
		_ = unix.SysvShmDetach(addr)
		_, _ = unix.SysvShmCtl(id, unix.IPC_RMID, nil)
		return
	}
	// Round-trip so the server has attached before we mark the segment removable.
	foc := []byte{opGetInputFocus, 0, 1, 0}
	if _, err := x.conn.Write(foc); err != nil {
		_ = unix.SysvShmDetach(addr)
		_, _ = unix.SysvShmCtl(id, unix.IPC_RMID, nil)
		return
	}
	if _, err := x.readReply(); err != nil {
		_ = unix.SysvShmDetach(addr)
		_, _ = unix.SysvShmCtl(id, unix.IPC_RMID, nil)
		return
	}
	// Mark for removal: the kernel frees it once both we and the server detach.
	_, _ = unix.SysvShmCtl(id, unix.IPC_RMID, nil)
	x.shmOK, x.shmOp, x.shmSeg, x.shmID, x.shmAddr = true, op, seg, id, addr
	log.Printf("rmm-remote: MIT-SHM enabled (%d bytes shared)", size)
}

// queryExtensionMajor returns an extension's major opcode and whether present.
func (x *x11) queryExtensionMajor(name string) (byte, bool) {
	nlen := len(name)
	req := make([]byte, 4+4+nlen+pad4(nlen))
	req[0] = opQueryExtension
	le.PutUint16(req[2:], uint16(len(req)/4))
	le.PutUint16(req[4:], uint16(nlen))
	copy(req[8:], name)
	if _, err := x.conn.Write(req); err != nil {
		return 0, false
	}
	h, err := x.readReply()
	if err != nil || h[8] == 0 {
		return 0, false
	}
	return h[9], true
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
	_ = u32()         // release number
	x.xidBase = u32() // resource-id base
	x.xidMask = u32() // resource-id mask
	_ = u32()         // motion buffer size
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
	x.screenW, x.screenH = x.width, x.height
	return nil
}

func (x *x11) close() {
	if x.shmAddr != nil {
		// Detach on the server (best-effort) then locally; the segment was already
		// marked IPC_RMID, so the kernel frees it once both sides detach.
		dt := make([]byte, 8)
		dt[0] = x.shmOp
		dt[1] = shmDetach
		le.PutUint16(dt[2:], 2)
		le.PutUint32(dt[4:], x.shmSeg)
		if x.conn != nil {
			_, _ = x.conn.Write(dt)
		}
		_ = unix.SysvShmDetach(x.shmAddr)
		x.shmAddr = nil
	}
	if x.conn != nil {
		x.conn.Close()
	}
}

// convertBGRX writes an X ZPixmap (BGRX, LSBFirst) buffer into x.img as RGBA.
func (x *x11) convertBGRX(src []byte) {
	dst := x.img.Pix
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

// getImage captures the whole root window into x.img. It uses MIT-SHM when
// available and falls back to the socket path on any SHM error (disabling SHM
// process-wide so reattaches don't retry a broken path).
func (x *x11) getImage() (*image.RGBA, error) {
	if x.shmOK {
		if err := x.getImageSHM(); err != nil {
			log.Printf("rmm-remote: SHM capture failed (%v) — falling back to socket GetImage", err)
			shmFailedAt.Store(time.Now().UnixNano())
			x.shmOK = false
			return nil, err // media loop reattaches; that attach uses the socket path (cooldown)
		}
		return x.img, nil
	}
	return x.getImageSocket()
}

// getImageSHM asks the server to write the root window into the shared segment,
// then reads the pixels straight from shared memory (no socket transfer). A read
// deadline guards against a hang if the request errors instead of replying.
func (x *x11) getImageSHM() error {
	req := make([]byte, 32)
	req[0] = x.shmOp
	req[1] = shmGetImage
	le.PutUint16(req[2:], 8)
	le.PutUint32(req[4:], x.root) // drawable
	le.PutUint16(req[8:], uint16(int16(x.originX)))
	le.PutUint16(req[10:], uint16(int16(x.originY)))
	le.PutUint16(req[12:], uint16(x.width))
	le.PutUint16(req[14:], uint16(x.height))
	le.PutUint32(req[16:], 0xffffffff) // plane mask
	req[20] = zPixmap                  // format
	le.PutUint32(req[24:], x.shmSeg)
	le.PutUint32(req[28:], 0) // offset
	_ = x.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer x.conn.SetReadDeadline(time.Time{})
	if _, err := x.conn.Write(req); err != nil {
		return err
	}
	if _, err := x.readReply(); err != nil { // reply carries no pixels (they're in shm)
		return err
	}
	x.convertBGRX(x.shmAddr)
	return nil
}

// getImageSocket captures by pulling the whole framebuffer over the X socket.
func (x *x11) getImageSocket() (*image.RGBA, error) {
	req := make([]byte, 20)
	req[0] = opGetImage
	req[1] = zPixmap
	le.PutUint16(req[2:], 5) // request length in 4-byte units
	le.PutUint32(req[4:], x.root)
	le.PutUint16(req[8:], uint16(int16(x.originX)))
	le.PutUint16(req[10:], uint16(int16(x.originY)))
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
	x.convertBGRX(x.raw)
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

func (x *x11) fakeMotion(px, py int) { x.fakeInput(evMotionNotify, 0, px, py) }
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

// ---- RandR monitors ----

// xMonitor is one physical display inside the single X screen. RandR stitches
// them into one root window, so capture needs the rectangle to crop to.
type xMonitor struct {
	X, Y, W, H int
	Primary    bool
}

const (
	randrQueryVersion = 0
	randrGetMonitors  = 42 // RandR 1.5
)

// queryRandR resolves the RandR opcode and confirms 1.5, which is where
// RRGetMonitors was introduced. The version handshake is not optional: an
// unsupported request answers with an X error, and readReply skips errors while
// waiting for a reply that will never come.
func (x *x11) queryRandR() {
	op, ok := x.queryExtensionMajor("RANDR")
	if !ok {
		return
	}
	req := make([]byte, 12)
	req[0] = op
	req[1] = randrQueryVersion
	le.PutUint16(req[2:], 3)
	le.PutUint32(req[4:], 1) // client major
	le.PutUint32(req[8:], 5) // client minor
	if _, err := x.conn.Write(req); err != nil {
		return
	}
	h, err := x.readReply()
	if err != nil {
		return
	}
	major, minor := le.Uint32(h[8:]), le.Uint32(h[12:])
	if major > 1 || (major == 1 && minor >= 5) {
		x.randrOp = op
	} else {
		log.Printf("rmm-remote: RandR %d.%d is older than 1.5 — per-monitor capture unavailable", major, minor)
	}
}

// monitors lists the displays left to right, matching the order the viewer
// shows and indexes them by. Empty when RandR is too old or reports nothing
// useful, which keeps the whole-screen capture as the only option.
func (x *x11) monitors() []xMonitor {
	if x.randrOp == 0 {
		return nil
	}
	req := make([]byte, 12)
	req[0] = x.randrOp
	req[1] = randrGetMonitors
	le.PutUint16(req[2:], 3)
	le.PutUint32(req[4:], x.root)
	req[8] = 1 // get-active: only monitors with a CRTC
	_ = x.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer x.conn.SetReadDeadline(time.Time{})
	if _, err := x.conn.Write(req); err != nil {
		return nil
	}
	h, err := x.readReply()
	if err != nil {
		return nil
	}
	body := make([]byte, int(le.Uint32(h[4:]))*4)
	if _, err := io.ReadFull(x.r, body); err != nil {
		return nil
	}
	count := int(le.Uint32(h[12:]))
	var out []xMonitor
	p := 0
	for i := 0; i < count; i++ {
		// MONITORINFO: name(4) primary(1) automatic(1) ncrtcs(2) x(2) y(2)
		// width(2) height(2) widthmm(4) heightmm(4) then ncrtcs × output(4).
		if p+24 > len(body) {
			break
		}
		ncrtcs := int(le.Uint16(body[p+6:]))
		out = append(out, xMonitor{
			X: int(int16(le.Uint16(body[p+8:]))), Y: int(int16(le.Uint16(body[p+10:]))),
			W: int(le.Uint16(body[p+12:])), H: int(le.Uint16(body[p+14:])),
			Primary: body[p+4] != 0,
		})
		p += 24 + 4*ncrtcs
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].X != out[j].X {
			return out[i].X < out[j].X
		}
		return out[i].Y < out[j].Y
	})
	return out
}

// setRegion points the capturer at one monitor, or at the whole screen when sel
// is 0. The frame buffer is rebuilt because the encoder keys its tile diff on
// the frame size; callers force a keyframe afterwards.
func (x *x11) setRegion(sel int32) {
	ox, oy, w, h := 0, 0, x.screenW, x.screenH
	if sel > 0 {
		if mons := x.monitors(); int(sel) <= len(mons) {
			m := mons[sel-1]
			// A monitor can extend past the root window after a hot-unplug.
			if m.W > 0 && m.H > 0 && m.X+m.W <= x.screenW && m.Y+m.H <= x.screenH {
				ox, oy, w, h = m.X, m.Y, m.W, m.H
			}
		}
	}
	if ox == x.originX && oy == x.originY && w == x.width && h == x.height {
		return
	}
	x.originX, x.originY, x.width, x.height = ox, oy, w, h
	x.img = image.NewRGBA(image.Rect(0, 0, w, h))
}
