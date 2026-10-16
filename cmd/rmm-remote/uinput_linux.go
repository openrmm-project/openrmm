//go:build linux

package main

// Kernel-level input injection via /dev/uinput. Unlike XTEST (which targets one
// X server and is unreliable on the GDM greeter), a uinput virtual device feeds
// the kernel input layer, so events reach whatever owns the active VT — the
// login greeter before login, the user's session after — and work on Xorg and
// Wayland alike. Requires root (the sidecar already runs as root to capture the
// greeter). Falls back to XTEST when /dev/uinput isn't available.

import (
	"time"

	"golang.org/x/sys/unix"
)

// uinput ioctls (amd64 Linux): _IO('U',n) / _IOW('U',n,4).
const (
	uiSetEvBit   = 0x40045564
	uiSetKeyBit  = 0x40045565
	uiSetRelBit  = 0x40045566
	uiSetAbsBit  = 0x40045567
	uiDevCreate  = 0x5501
	uiDevDestroy = 0x5502

	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03

	synReport = 0x00
	relWheel  = 0x08
	absXCode  = 0x00
	absYCode  = 0x01

	btnLeft   = 0x110
	btnRight  = 0x111
	btnMiddle = 0x112

	absRange = 0x7fff // absolute axis 0..32767, mapped proportionally to the screen
)

// uinputDeviceName labels our virtual injection device. inputlock_linux.go reads
// it back from /proc/bus/input/devices to EXCLUDE this device from the grab, so
// the technician's injected input is never blocked.
const uinputDeviceName = "rmm-remote virtual input"

type uinputDevice struct{ fd int }

// newUinput creates a virtual pointer+keyboard device.
func newUinput() (*uinputDevice, error) {
	fd, err := unix.Open("/dev/uinput", unix.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	bail := func(e error) (*uinputDevice, error) { unix.Close(fd); return nil, e }

	if err := unix.IoctlSetInt(fd, uiSetEvBit, evKey); err != nil {
		return bail(err)
	}
	_ = unix.IoctlSetInt(fd, uiSetEvBit, evRel)
	_ = unix.IoctlSetInt(fd, uiSetEvBit, evAbs)
	_ = unix.IoctlSetInt(fd, uiSetEvBit, evSyn)
	for _, b := range []int{btnLeft, btnRight, btnMiddle} {
		_ = unix.IoctlSetInt(fd, uiSetKeyBit, b)
	}
	for _, kc := range browserKeyEvdev {
		_ = unix.IoctlSetInt(fd, uiSetKeyBit, int(kc))
	}
	_ = unix.IoctlSetInt(fd, uiSetRelBit, relWheel)
	_ = unix.IoctlSetInt(fd, uiSetAbsBit, absXCode)
	_ = unix.IoctlSetInt(fd, uiSetAbsBit, absYCode)

	// Legacy uinput_user_dev: name[80] + input_id(8) + ff_effects_max(4) +
	// absmax/absmin/absfuzz/absflat ([64]int32 each) = 1116 bytes.
	dev := make([]byte, 1116)
	copy(dev[0:80], uinputDeviceName)
	le.PutUint16(dev[80:], 0x06) // BUS_VIRTUAL
	le.PutUint16(dev[82:], 0x1234)
	le.PutUint16(dev[84:], 0x5678)
	le.PutUint16(dev[86:], 1)
	le.PutUint32(dev[92:], absRange) // absmax[ABS_X]
	le.PutUint32(dev[96:], absRange) // absmax[ABS_Y]
	if _, err := unix.Write(fd, dev); err != nil {
		return bail(err)
	}
	if err := unix.IoctlSetInt(fd, uiDevCreate, 0); err != nil {
		return bail(err)
	}
	time.Sleep(250 * time.Millisecond) // let udev/Xorg hotplug the new device
	return &uinputDevice{fd: fd}, nil
}

func (u *uinputDevice) emit(typ, code uint16, value int32) {
	var ev [24]byte // struct input_event on amd64: timeval(16) + type(2) + code(2) + value(4)
	le.PutUint16(ev[16:], typ)
	le.PutUint16(ev[18:], code)
	le.PutUint32(ev[20:], uint32(value))
	_, _ = unix.Write(u.fd, ev[:])
}

func (u *uinputDevice) syn() { u.emit(evSyn, synReport, 0) }

func (u *uinputDevice) moveAbs(nx, ny float64) {
	u.emit(evAbs, absXCode, int32(clamp01(nx)*absRange))
	u.emit(evAbs, absYCode, int32(clamp01(ny)*absRange))
	u.syn()
}

func (u *uinputDevice) button(code uint16, down bool) { u.emit(evKey, code, b2i(down)); u.syn() }
func (u *uinputDevice) key(code uint16, down bool)    { u.emit(evKey, code, b2i(down)); u.syn() }

func (u *uinputDevice) wheel(up bool) {
	v := int32(-1)
	if up {
		v = 1
	}
	u.emit(evRel, relWheel, v)
	u.syn()
}

func (u *uinputDevice) close() {
	_ = unix.IoctlSetInt(u.fd, uiDevDestroy, 0)
	_ = unix.Close(u.fd)
}

func b2i(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// browserKeyEvdev maps DOM keyCodes to Linux evdev KEY_* codes (US layout).
var browserKeyEvdev = map[int]uint16{
	8: 14, 9: 15, 13: 28, 16: 42, 17: 29, 18: 56, 20: 58, 27: 1, 32: 57,
	33: 104, 34: 109, 35: 107, 36: 102, 37: 105, 38: 103, 39: 106, 40: 108, 46: 111,
	48: 11, 49: 2, 50: 3, 51: 4, 52: 5, 53: 6, 54: 7, 55: 8, 56: 9, 57: 10,
	65: 30, 66: 48, 67: 46, 68: 32, 69: 18, 70: 33, 71: 34, 72: 35, 73: 23, 74: 36, 75: 37, 76: 38, 77: 50,
	78: 49, 79: 24, 80: 25, 81: 16, 82: 19, 83: 31, 84: 20, 85: 22, 86: 47, 87: 17, 88: 45, 89: 21, 90: 44,
	112: 59, 113: 60, 114: 61, 115: 62, 116: 63, 117: 64, 118: 65, 119: 66, 120: 67, 121: 68, 122: 87, 123: 88,
	186: 39, 187: 13, 188: 51, 189: 12, 190: 52, 191: 53, 192: 41, 219: 26, 220: 43, 221: 27, 222: 40,
}
