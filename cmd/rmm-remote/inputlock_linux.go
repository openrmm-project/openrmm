//go:build linux

package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Input lock (Linux): while enabled, the host's PHYSICAL keyboard and mouse are
// taken away from everyone via the evdev EVIOCGRAB ioctl, which makes the
// grabbing process the exclusive consumer of each device. The technician's input
// still lands because it is injected through a SEPARATE path — the /dev/uinput
// virtual device (or XTEST) — which the grab doesn't touch.
//
// Because the grab is at the kernel input layer, it is independent of which X
// server is active, so the lock holds across the seat-following transitions
// (display-manager greeter ↔ user session) WITHOUT re-arming, and it also covers
// the login screen. Failsafe: the kernel releases a grab when the fd is closed,
// so if the sidecar dies the host's input is restored automatically.
const eviocgrab = 0x40044590 // _IOW('E', 0x90, int)

// Bit positions in a device's EV capability mask (B: EV= in
// /proc/bus/input/devices). evKey/evRel/evAbs come from uinput_linux.go.
const (
	evKeyBit = 1 << evKey // keyboards + pointer buttons
	evRelBit = 1 << evRel // relative axes (mice)
	evAbsBit = 1 << evAbs // absolute axes (touchpads/tablets)
)

// inputLock holds the grabbed device fds for the duration of a lock.
type inputLock struct {
	mu  sync.Mutex
	fds []int
	on  bool
}

// set grabs (on) or releases (off) every physical keyboard/pointer device.
func (l *inputLock) set(on bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if on == l.on {
		return nil
	}
	if !on {
		l.release()
		return nil
	}
	nodes := physicalInputNodes()
	if len(nodes) == 0 {
		return fmt.Errorf("no physical input devices found")
	}
	var fds []int
	for _, n := range nodes {
		fd, err := unix.Open(n, unix.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		if err := unix.IoctlSetInt(fd, eviocgrab, 1); err != nil {
			unix.Close(fd) // device busy (already grabbed by X) or no permission
			continue
		}
		fds = append(fds, fd)
	}
	if len(fds) == 0 {
		return fmt.Errorf("could not grab any input device (need root)")
	}
	l.fds = fds
	l.on = true
	log.Printf("inputlock: grabbed %d physical input device(s)", len(fds))
	return nil
}

// release ungrabs and closes every held device. Closing the fd alone already
// releases the grab (kernel failsafe); we ungrab explicitly first.
func (l *inputLock) release() {
	for _, fd := range l.fds {
		_ = unix.IoctlSetInt(fd, eviocgrab, 0)
		_ = unix.Close(fd)
	}
	if l.on {
		log.Printf("inputlock: released physical input")
	}
	l.fds = nil
	l.on = false
}

// closeAll is the session-end failsafe (deferred): always restore input.
func (l *inputLock) closeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.release()
}

// physicalInputNodes returns the /dev/input/eventN paths of real keyboards and
// pointers, skipping power buttons, lid switches, etc. It reads
// /proc/bus/input/devices and selects a device when it exposes a keyboard
// handler ("kbd") or its capability mask has EV_KEY together with EV_REL/EV_ABS
// (a mouse or touchpad) — which excludes EV_KEY-only devices like power buttons.
func physicalInputNodes() []string {
	f, err := os.Open("/proc/bus/input/devices")
	if err != nil {
		return nil
	}
	defer f.Close()

	var nodes []string
	var handlers, name string
	var ev uint64
	flush := func() {
		// Never grab our OWN uinput injection device — it shows up here as a
		// keyboard+pointer, and grabbing it would swallow the technician's
		// injected input (the exact bug this guards against).
		if !strings.Contains(name, uinputDeviceName) {
			isKbd := strings.Contains(handlers, "kbd")
			isPtr := ev&evKeyBit != 0 && ev&(evRelBit|evAbsBit) != 0
			if isKbd || isPtr {
				for _, tok := range strings.Fields(handlers) {
					if strings.HasPrefix(tok, "event") {
						nodes = append(nodes, "/dev/input/"+tok)
					}
				}
			}
		}
		handlers, name, ev = "", "", 0
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "": // blank line separates device blocks
			flush()
		case strings.HasPrefix(line, "N: Name="):
			name = strings.TrimPrefix(line, "N: Name=")
		case strings.HasPrefix(line, "H: Handlers="):
			handlers = strings.TrimPrefix(line, "H: Handlers=")
		case strings.HasPrefix(line, "B: EV="):
			if v, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "B: EV=")), 16, 64); err == nil {
				ev = v
			}
		}
	}
	flush() // final block (the file may not end with a blank line)
	return nodes
}
