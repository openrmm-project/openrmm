//go:build linux

package main

// Native in-session chat window for the Linux host — the X11 analogue of the
// Win32 chat window (chat_windows.go). It opens its OWN X11 connection (separate
// from the capture connection, which is busy with GetImage and gets swapped on
// seat changes), draws a simple message log + input line with core-X requests
// (no toolkit, no CGO, no deps), and reads KeyPress/Expose events. Because it
// talks raw X11 it works on any X server the sidecar can reach — including the
// display-manager greeter — exactly like the capture path.
//
// Wire parity with Windows: the viewer sends {t:"chat",from:"tech",text}; we
// show it and let the host user type a reply, which we hand back via onSend as
// {t:"chat",from:"user",text}.

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
)

// X core request opcodes used here.
const (
	opCreateWindow    = 1
	opMapWindow       = 8
	opConfigureWindow = 12
	opInternAtom      = 16
	opChangeProperty  = 18
	opOpenFont        = 45
	opCreateGC        = 55
	opClearArea       = 61
	opImageText8      = 76

	xaWMName         = 39 // predefined atom WM_NAME
	xaString         = 31 // predefined atom STRING
	xaAtom           = 4  // predefined atom ATOM
	xaCardinal       = 6  // predefined atom CARDINAL
	evtKeyPress      = 2
	evtButtonPress   = 4
	evtButtonRelease = 5
	evtMotionNotify  = 6
	evtExpose        = 12

	chatW, chatH = 420, 320
	chatLineH    = 15
	chatPad      = 8
	chatMaxLines = 17 // visible message lines above the input row
)

type chatLinux struct {
	self string // how the person here is named (selfLabel)
	conn net.Conn
	r    *bufio.Reader

	white, black uint32
	root, win    uint32
	gc, font     uint32
	xidBase      uint32
	xidMask      uint32
	xidNext      uint32

	// keycode → keysym table (col 0 unshifted, col 1 shifted).
	minKey  int
	perCode int
	keysyms []uint32

	mu     sync.Mutex
	msgs   []string
	input  []rune
	closed bool
	onSend func(string)

	// Drag state: click-and-drag anywhere on the window moves it (many WMs give
	// this utility window no title bar, so there's no other way to reposition it).
	dragging bool
	dragX    int16 // pointer offset within the window at grab time
	dragY    int16
}

// newChatWindowLinux opens and maps a chat window on the given display/auth.
// Returns nil (no error surfaced) if anything in the X handshake fails — chat is
// best-effort and must never break the remote session.
func newChatWindowLinux(display, authPath, title string, onSend func(string)) *chatLinux {
	conn, dnum, err := dialDisplay(display)
	if err != nil {
		return nil
	}
	name, data := authFromFile(authPath, dnum)
	c := &chatLinux{conn: conn, onSend: onSend, self: selfLabel(authAccount(authPath))}
	if !c.handshake(name, data) {
		conn.Close()
		return nil
	}
	c.loadKeymap()
	// Resolve the EWMH atoms we use to avoid stealing focus (reply-bearing
	// requests — do them before the no-reply window setup below).
	userTime := c.internAtom("_NET_WM_USER_TIME")
	winType := c.internAtom("_NET_WM_WINDOW_TYPE")
	utility := c.internAtom("_NET_WM_WINDOW_TYPE_UTILITY")
	wmIcon := c.internAtom("_NET_WM_ICON")

	c.font = c.newXID()
	c.openFont(c.font, "fixed")
	c.win = c.newXID()
	c.createWindow()
	c.gc = c.newXID()
	c.createGC()
	c.setTitle(title)
	if icon := embeddedIcon(); wmIcon != 0 && len(icon) > 0 {
		c.write(changePropCardinals(c.win, wmIcon, icon))
	}
	// Don't grab keyboard focus when the window maps: the technician injects
	// input into whatever host window is focused, so a chat window that stole
	// focus would swallow their keystrokes until they clicked back on the
	// desktop. _NET_WM_USER_TIME=0 tells the WM "do not focus me on map"; the
	// UTILITY type marks it auxiliary. The host user can still click to type.
	if userTime != 0 {
		c.changeProp32(userTime, xaCardinal, 0)
	}
	if winType != 0 && utility != 0 {
		c.changeProp32(winType, xaAtom, utility)
	}
	c.mapWindow()
	c.msgs = []string{"Connected. The technician can chat here.", ""}
	go c.eventLoop()
	c.redraw()
	return c
}

func (c *chatLinux) newXID() uint32 {
	id := c.xidBase | (c.xidNext & c.xidMask)
	c.xidNext++
	return id
}

// handshake performs the X11 setup and keeps the few screen fields we need
// (root window, white/black pixel) plus the resource-id allocator range.
func (c *chatLinux) handshake(name, data []byte) bool {
	req := make([]byte, 0, 64)
	req = append(req, 'l', 0)
	req = le.AppendUint16(req, 11)
	req = le.AppendUint16(req, 0)
	req = le.AppendUint16(req, uint16(len(name)))
	req = le.AppendUint16(req, uint16(len(data)))
	req = le.AppendUint16(req, 0)
	req = append(req, name...)
	req = append(req, make([]byte, pad4(len(name)))...)
	req = append(req, data...)
	req = append(req, make([]byte, pad4(len(data)))...)
	if _, err := c.conn.Write(req); err != nil {
		return false
	}
	c.r = bufio.NewReaderSize(c.conn, 1<<15)
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(c.r, hdr); err != nil {
		return false
	}
	b := make([]byte, int(le.Uint16(hdr[6:8]))*4)
	if _, err := io.ReadFull(c.r, b); err != nil || hdr[0] != 1 {
		return false
	}
	if len(b) < 40 {
		return false
	}
	p := 0
	u32 := func() uint32 { v := le.Uint32(b[p:]); p += 4; return v }
	u16 := func() uint16 { v := le.Uint16(b[p:]); p += 2; return v }
	u8 := func() byte { v := b[p]; p++; return v }
	_ = u32()          // release number
	c.xidBase = u32()  // resource-id base
	c.xidMask = u32()  // resource-id mask
	_ = u32()          // motion buffer size
	vlen := int(u16()) // vendor length
	_ = u16()          // max request length
	_ = u8()           // number of screens
	nfmt := int(u8())  // number of pixmap formats
	_ = u8()           // image byte order
	_ = u8()           // bitmap bit order
	_ = u8()           // scanline unit
	_ = u8()           // scanline pad
	c.minKey = int(u8())
	_ = u8() // max keycode
	p += 4   // unused
	p += vlen + pad4(vlen)
	p += 8 * nfmt
	if p+40 > len(b) {
		return false
	}
	c.root = u32()
	_ = u32() // default colormap
	c.white = u32()
	c.black = u32()
	return true
}

func (c *chatLinux) loadKeymap() {
	if c.minKey == 0 {
		return
	}
	// Ask for a generous range; the server reports keysyms-per-keycode.
	count := 256 - c.minKey
	if count > 248 {
		count = 248
	}
	req := make([]byte, 8)
	req[0] = opGetKeyboardMapping
	le.PutUint16(req[2:], 2)
	req[4] = byte(c.minKey)
	req[5] = byte(count)
	if _, err := c.conn.Write(req); err != nil {
		return
	}
	h := make([]byte, 32)
	if _, err := io.ReadFull(c.r, h); err != nil || h[0] != 1 {
		return
	}
	c.perCode = int(h[1])
	n := int(le.Uint32(h[4:8])) * 4
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return
	}
	c.keysyms = make([]uint32, n/4)
	for i := range c.keysyms {
		c.keysyms[i] = le.Uint32(buf[i*4:])
	}
}

// keysym resolves a keycode + shift state to an X keysym via the loaded table.
func (c *chatLinux) keysym(keycode int, shift bool) uint32 {
	if c.perCode == 0 || keycode < c.minKey {
		return 0
	}
	base := (keycode - c.minKey) * c.perCode
	col := 0
	if shift && c.perCode > 1 {
		col = 1
	}
	if base+col >= len(c.keysyms) {
		return 0
	}
	ks := c.keysyms[base+col]
	if ks == 0 && col == 1 {
		ks = c.keysyms[base] // no shifted form → fall back to base
	}
	return ks
}

// ---- X requests ----

func (c *chatLinux) createWindow() {
	// Exposure | KeyPress | ButtonPress | ButtonRelease | Button1Motion — the
	// button/motion bits let us implement drag-to-move (see eventLoop).
	const eventMask = 0x8000 | 0x0001 | 0x0004 | 0x0008 | 0x0100
	req := make([]byte, 40)
	req[0] = opCreateWindow
	req[1] = 0 // depth = CopyFromParent
	le.PutUint16(req[2:], 10)
	le.PutUint32(req[4:], c.win)
	le.PutUint32(req[8:], c.root)
	// x,y = 60,60; w,h; border 0; class InputOutput(1); visual CopyFromParent(0)
	le.PutUint16(req[12:], 60)
	le.PutUint16(req[14:], 60)
	le.PutUint16(req[16:], chatW)
	le.PutUint16(req[18:], chatH)
	le.PutUint16(req[20:], 0)
	le.PutUint16(req[22:], 1)
	le.PutUint32(req[24:], 0)
	le.PutUint32(req[28:], 0x0002|0x0800) // BackPixel | EventMask
	le.PutUint32(req[32:], c.white)       // background pixel
	le.PutUint32(req[36:], eventMask)
	c.write(req)
}

// moveWindow repositions the window to root coordinates (x,y) via ConfigureWindow.
func (c *chatLinux) moveWindow(x, y int16) {
	req := make([]byte, 20)
	req[0] = opConfigureWindow
	le.PutUint16(req[2:], 5) // request length in 4-byte units
	le.PutUint32(req[4:], c.win)
	le.PutUint16(req[8:], 0x0003) // value-mask: X | Y
	le.PutUint32(req[12:], uint32(int32(x)))
	le.PutUint32(req[16:], uint32(int32(y)))
	c.write(req)
}

func (c *chatLinux) createGC() {
	req := make([]byte, 28)
	req[0] = opCreateGC
	le.PutUint16(req[2:], 7)
	le.PutUint32(req[4:], c.gc)
	le.PutUint32(req[8:], c.win)
	le.PutUint32(req[12:], 0x0004|0x0008|0x4000) // Foreground | Background | Font
	le.PutUint32(req[16:], c.black)
	le.PutUint32(req[20:], c.white)
	le.PutUint32(req[24:], c.font)
	c.write(req)
}

func (c *chatLinux) openFont(fid uint32, fname string) {
	nb := []byte(fname)
	body := 12 + len(nb) + pad4(len(nb))
	req := make([]byte, body)
	req[0] = opOpenFont
	le.PutUint16(req[2:], uint16(body/4))
	le.PutUint32(req[4:], fid)
	le.PutUint16(req[8:], uint16(len(nb)))
	copy(req[12:], nb)
	c.write(req)
}

// internAtom resolves an atom name to its id (reply-bearing; call during setup
// before the event loop). Returns 0 on failure — callers skip the property.
func (c *chatLinux) internAtom(name string) uint32 {
	nb := []byte(name)
	body := 8 + len(nb) + pad4(len(nb))
	req := make([]byte, body)
	req[0] = opInternAtom
	req[1] = 0 // only-if-exists = false (create)
	le.PutUint16(req[2:], uint16(body/4))
	le.PutUint16(req[4:], uint16(len(nb)))
	copy(req[8:], nb)
	if _, err := c.conn.Write(req); err != nil {
		return 0
	}
	h := make([]byte, 32)
	if _, err := io.ReadFull(c.r, h); err != nil || h[0] != 1 {
		return 0
	}
	return le.Uint32(h[8:])
}

// changeProp32 replaces a single 32-bit window property.
func (c *chatLinux) changeProp32(prop, typ, val uint32) {
	req := make([]byte, 28)
	req[0] = opChangeProperty
	req[1] = 0 // Replace
	le.PutUint16(req[2:], 7)
	le.PutUint32(req[4:], c.win)
	le.PutUint32(req[8:], prop)
	le.PutUint32(req[12:], typ)
	req[16] = 32 // format
	le.PutUint32(req[20:], 1)
	le.PutUint32(req[24:], val)
	c.write(req)
}

func (c *chatLinux) mapWindow() {
	req := make([]byte, 8)
	req[0] = opMapWindow
	le.PutUint16(req[2:], 2)
	le.PutUint32(req[4:], c.win)
	c.write(req)
}

func (c *chatLinux) setTitle(title string) {
	tb := []byte(title)
	body := 24 + len(tb) + pad4(len(tb))
	req := make([]byte, body)
	req[0] = opChangeProperty
	req[1] = 0 // Replace
	le.PutUint16(req[2:], uint16(body/4))
	le.PutUint32(req[4:], c.win)
	le.PutUint32(req[8:], xaWMName)
	le.PutUint32(req[12:], xaString)
	req[16] = 8 // format
	le.PutUint32(req[20:], uint32(len(tb)))
	copy(req[24:], tb)
	c.write(req)
}

func (c *chatLinux) clearArea() {
	req := make([]byte, 16)
	req[0] = opClearArea
	le.PutUint16(req[2:], 4)
	le.PutUint32(req[4:], c.win)
	// x,y,w,h all zero-origin/full → whole window (w=h=0 means "to window edge")
	c.write(req)
}

func (c *chatLinux) imageText(x, y int, s string) {
	bs := latin1(s)
	if len(bs) > 255 {
		bs = bs[:255]
	}
	body := 16 + len(bs) + pad4(len(bs))
	req := make([]byte, body)
	req[0] = opImageText8
	req[1] = byte(len(bs))
	le.PutUint16(req[2:], uint16(body/4))
	le.PutUint32(req[4:], c.win)
	le.PutUint32(req[8:], c.gc)
	le.PutUint16(req[12:], uint16(int16(x)))
	le.PutUint16(req[14:], uint16(int16(y)))
	copy(req[16:], bs)
	c.write(req)
}

func (c *chatLinux) write(b []byte) {
	if c.conn != nil {
		_, _ = c.conn.Write(b)
	}
}

// ---- drawing & events ----

// redraw repaints the whole window: the last N messages, then the input row.
func (c *chatLinux) redraw() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.clearArea()
	start := 0
	if len(c.msgs) > chatMaxLines {
		start = len(c.msgs) - chatMaxLines
	}
	y := chatPad + chatLineH
	for _, m := range c.msgs[start:] {
		color := c.black
		if strings.HasPrefix(m, "Tech: ") {
			color = 0x7135a8
			m = "Technician: " + strings.TrimPrefix(m, "Tech: ")
		}
		if strings.HasPrefix(m, "You: ") {
			color = 0x146b38
			m = c.self + ": " + strings.TrimPrefix(m, "You: ")
		}
		if strings.HasPrefix(m, chatGuestPrefix) {
			color = 0xa15c00 // amber: a guest, neither the technician nor the person here
			m = strings.TrimPrefix(m, chatGuestPrefix)
		}
		c.foreground(color)
		c.imageText(chatPad, y, clip(m, 68))
		y += chatLineH
	}
	c.foreground(0x146b38)
	c.imageText(chatPad, chatH-chatPad, clip(c.self+" > "+string(c.input), 68))
}

func (c *chatLinux) foreground(color uint32) {
	req := make([]byte, 16)
	req[0] = 56 // ChangeGC
	le.PutUint16(req[2:], 4)
	le.PutUint32(req[4:], c.gc)
	le.PutUint32(req[8:], 0x0004) // GCForeground
	le.PutUint32(req[12:], color)
	c.write(req)
}

// latin1 maps a UTF-8 string to ISO-8859-1 bytes, which is what core-X text
// requests (ImageText8 / WM_NAME STRING) expect. Code points beyond Latin-1
// (accents above 0xff, emoji, the em dash) become '?' instead of multi-byte
// garbage glyphs.
func latin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r <= 0xff {
			out = append(out, byte(r))
		} else {
			out = append(out, '?')
		}
	}
	return out
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// append adds a line and repaints. Safe to call from the capture goroutine.
func (c *chatLinux) append(line string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.msgs = append(c.msgs, line)
	c.mu.Unlock()
	c.redraw()
}

func (c *chatLinux) eventLoop() {
	ev := make([]byte, 32)
	for {
		if _, err := io.ReadFull(c.r, ev); err != nil {
			c.mu.Lock()
			c.closed = true
			c.mu.Unlock()
			return
		}
		switch ev[0] & 0x7f {
		case evtExpose:
			// Redraw once per expose burst (count==0 is the last rectangle).
			if le.Uint16(ev[16:]) == 0 {
				c.redraw()
			}
		case evtKeyPress:
			c.onKey(int(ev[1]), le.Uint16(ev[28:])&0x01 != 0)
		case evtButtonPress:
			// Start a drag: remember where inside the window we grabbed (event-x/y).
			if ev[1] == 1 { // button 1
				c.dragging = true
				c.dragX = int16(le.Uint16(ev[24:]))
				c.dragY = int16(le.Uint16(ev[26:]))
			}
		case evtButtonRelease:
			c.dragging = false
		case evtMotionNotify:
			// Move the window so the grab point stays under the pointer
			// (root-x/y minus the in-window offset).
			if c.dragging {
				c.moveWindow(int16(le.Uint16(ev[20:]))-c.dragX, int16(le.Uint16(ev[22:]))-c.dragY)
			}
		}
	}
}

// onKey edits the input line; Return submits it via onSend.
func (c *chatLinux) onKey(keycode int, shift bool) {
	ks := c.keysym(keycode, shift)
	switch {
	case ks == 0xff0d || ks == 0xff8d: // Return / KP_Enter
		c.mu.Lock()
		text := string(c.input)
		c.input = nil
		if text != "" {
			c.msgs = append(c.msgs, "You: "+text)
		}
		send := c.onSend
		c.mu.Unlock()
		if text != "" && send != nil {
			send(text)
		}
		c.redraw()
	case ks == 0xff08: // BackSpace
		c.mu.Lock()
		if n := len(c.input); n > 0 {
			c.input = c.input[:n-1]
		}
		c.mu.Unlock()
		c.redraw()
	case ks == 0x20 || (ks >= 0x21 && ks <= 0x7e) || (ks >= 0xa0 && ks <= 0xff):
		c.mu.Lock()
		c.input = append(c.input, rune(ks))
		c.mu.Unlock()
		c.redraw()
	}
}

func (c *chatLinux) close() {
	c.mu.Lock()
	c.closed = true
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// isClosed reports whether the window is gone — closed by us or by the user
// clicking the WM close button (which kills the client and ends eventLoop).
func (c *chatLinux) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
