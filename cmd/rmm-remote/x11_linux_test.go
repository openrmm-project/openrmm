//go:build linux

package main

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPad4(t *testing.T) {
	for in, want := range map[int]int{0: 0, 1: 3, 2: 2, 3: 1, 4: 0, 5: 3} {
		if got := pad4(in); got != want {
			t.Errorf("pad4(%d)=%d want %d", in, got, want)
		}
	}
}

func TestFakeInputWireLayout(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	x := &x11{conn: c1, xtestOp: 0x80, root: 0xdeadbeef}

	go x.fakeMotion(100, 200)
	buf := make([]byte, 40)
	if _, err := io.ReadFull(c2, buf); err != nil {
		t.Fatal(err)
	}
	if buf[0] != 0x80 || buf[1] != 2 {
		t.Errorf("opcode/minor = %#x/%d, want 0x80/2", buf[0], buf[1])
	}
	if l := binary.LittleEndian.Uint16(buf[2:]); l != 10 {
		t.Errorf("length=%d units, want 10", l)
	}
	if buf[4] != evMotionNotify {
		t.Errorf("type=%d, want MotionNotify(%d)", buf[4], evMotionNotify)
	}
	if r := binary.LittleEndian.Uint32(buf[12:]); r != 0xdeadbeef {
		t.Errorf("root=%#x, want 0xdeadbeef", r)
	}
	if gx := int16(binary.LittleEndian.Uint16(buf[24:])); gx != 100 {
		t.Errorf("rootX=%d, want 100", gx)
	}
	if gy := int16(binary.LittleEndian.Uint16(buf[26:])); gy != 200 {
		t.Errorf("rootY=%d, want 200", gy)
	}
}

func TestFakeInputDisabledWithoutXTest(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	x := &x11{conn: c1, xtestOp: 0} // XTEST unavailable → no-op
	done := make(chan struct{})
	go func() { x.fakeButton(1, true); close(done) }()
	select {
	case <-done: // returned without writing
	case <-readSome(c2):
		t.Fatal("fakeInput wrote despite XTEST being unavailable")
	}
}

func readSome(c net.Conn) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		b := make([]byte, 1)
		if _, err := c.Read(b); err == nil {
			close(ch)
		}
	}()
	return ch
}

func TestXauthParsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".Xauthority")
	cookie := []byte("0123456789abcdef") // 16 bytes
	var b []byte
	put := func(s []byte) {
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(s)))
		b = append(b, l[:]...)
		b = append(b, s...)
	}
	b = append(b, 0x01, 0x00)         // family
	put([]byte("host"))               // address
	put([]byte("0"))                  // display number
	put([]byte("MIT-MAGIC-COOKIE-1")) // name
	put(cookie)                       // data
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAUTHORITY", path)
	name, data := xauth(0)
	if string(name) != "MIT-MAGIC-COOKIE-1" {
		t.Errorf("name=%q", name)
	}
	if string(data) != string(cookie) {
		t.Errorf("data=%q want %q", data, cookie)
	}
}

func TestBrowserKeysymTable(t *testing.T) {
	cases := map[int]uint32{
		65:  0x61,   // 'A' key → keysym 'a'
		90:  0x7a,   // 'Z' → 'z'
		48:  0x30,   // '0'
		57:  0x39,   // '9'
		13:  0xff0d, // Enter
		112: 0xffbe, // F1
		123: 0xffc9, // F12
	}
	for code, want := range cases {
		if got := browserKeysym[code]; got != want {
			t.Errorf("browserKeysym[%d]=%#x want %#x", code, got, want)
		}
	}
}
