//go:build windows

package main

import (
	"log"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Input lock: while enabled, the host's PHYSICAL keyboard and mouse are blocked
// so the person at the machine can't interfere with the technician. Injected
// input (the technician's SendInput from desktopWorker) carries the INJECTED
// flag and is passed through, so remote control keeps working.
//
// Implemented with system-wide low-level hooks (WH_KEYBOARD_LL / WH_MOUSE_LL)
// rather than BlockInput, because BlockInput is dropped on every desktop switch
// and is unreliable on the secure/Winlogon desktops. The hooks are installed by
// a thread that FOLLOWS the active input desktop (like desktopWorker), so the
// lock holds on the user desktop, the UAC/secure desktop AND the login screen.
//
// Not covered: Ctrl+Alt+Del (the Secure Attention Sequence) is a kernel trusted
// path that no user-mode hook can intercept — suppressing it needs the
// Enterprise/IoT Keyboard Filter driver and is intentionally out of scope here.
var (
	procSetWindowsHookEx    = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procPeekMessage         = user32.NewProc("PeekMessageW")
	procMsgWaitForObjects   = user32.NewProc("MsgWaitForMultipleObjects")
)

const (
	whKeyboardLL  = 13
	whMouseLL     = 14
	hcAction      = 0
	llkhfInjected = 0x10 // KBDLLHOOKSTRUCT.flags: synthesized via SendInput
	llmhfInjected = 0x01 // MSLLHOOKSTRUCT.flags: synthesized via SendInput
	pmRemove      = 0x0001
	qsAllInput    = 0x04FF
)

type kbdllHookStruct struct {
	vkCode      uint32
	scanCode    uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

type msllHookStruct struct {
	pt          point
	mouseData   uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

// Hook callbacks are created once and reused (NewCallback allocations are never
// freed). They swallow physical events (return non-zero) and pass injected ones
// to the next hook so the technician's own input still lands.
var (
	kbHookCB = syscall.NewCallback(func(nCode, wParam, lParam uintptr) uintptr {
		if int32(nCode) == hcAction {
			ks := (*kbdllHookStruct)(unsafe.Pointer(lParam))
			if ks.flags&llkhfInjected == 0 {
				return 1 // physical key — block it
			}
		}
		r, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
		return r
	})
	mouseHookCB = syscall.NewCallback(func(nCode, wParam, lParam uintptr) uintptr {
		if int32(nCode) == hcAction {
			ms := (*msllHookStruct)(unsafe.Pointer(lParam))
			if ms.flags&llmhfInjected == 0 {
				return 1 // physical mouse — block it
			}
		}
		r, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
		return r
	})
)

// inputLockWorker runs for the whole session on its own OS thread. It idles
// until a viewer asks for the lock, then installs the LL hooks on the active
// input desktop and re-installs them whenever the desktop switches (so the lock
// follows the user → UAC/secure → Winlogon login transitions). Hooks are torn
// down when the lock is released and on session end (so input is always
// restored if the technician disconnects).
func (s *session) inputLockWorker() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInst, _, _ := procGetModuleH.Call(0)
	var kbHook, mouseHook, curDesk uintptr
	var curName string

	unhook := func() {
		if kbHook != 0 {
			procUnhookWindowsHookEx.Call(kbHook)
			kbHook = 0
		}
		if mouseHook != 0 {
			procUnhookWindowsHookEx.Call(mouseHook)
			mouseHook = 0
		}
	}
	// detach drops the hooks and the desktop handle — full release.
	detach := func() {
		unhook()
		if curDesk != 0 {
			procCloseDesktop.Call(curDesk)
			curDesk = 0
		}
		curName = ""
	}
	defer detach()

	var msg msgStruct
	for {
		select {
		case <-s.done:
			return
		default:
		}

		if !s.lockWanted.Load() {
			if kbHook != 0 || curDesk != 0 {
				detach()
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Ensure the hooks live on whichever desktop is currently receiving input.
		h, _, _ := procOpenInputDesktop.Call(0, 0, genericAll)
		if h != 0 {
			name := desktopName(h)
			if kbHook == 0 || name != curName {
				// SetThreadDesktop fails if this thread still owns hooks, so drop
				// them before switching, then re-arm on the new desktop.
				unhook()
				if r, _, _ := procSetThreadDesktop.Call(h); r != 0 {
					if curDesk != 0 {
						procCloseDesktop.Call(curDesk)
					}
					curDesk, curName = h, name
					kbHook, _, _ = procSetWindowsHookEx.Call(whKeyboardLL, kbHookCB, hInst, 0)
					mouseHook, _, _ = procSetWindowsHookEx.Call(whMouseLL, mouseHookCB, hInst, 0)
				} else {
					procCloseDesktop.Call(h) // couldn't attach; retry next tick
				}
			} else {
				procCloseDesktop.Call(h)
			}
		}

		// Pump messages so the LL hooks fire; wake at least every 200ms to
		// re-check the input desktop. The hook procs are trivial, so we stay
		// well under LowLevelHooksTimeout.
		procMsgWaitForObjects.Call(0, 0, 0, 200, qsAllInput)
		for {
			if r, _, _ := procPeekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, pmRemove); r == 0 {
				break
			}
		}
	}
}

// setInputLock toggles the physical-input block and acks the viewer so the UI
// reflects the real state. The host is told via the chat window (transparency).
func (s *session) setInputLock(on bool) {
	s.lockWanted.Store(on)
	log.Printf("inputlock: physical keyboard/mouse %s", map[bool]string{true: "blocked", false: "released"}[on])
	s.sendJSON(map[string]any{"t": "inputlocked", "enabled": on})
	if s.chat != nil {
		if on {
			s.chat.append("⚠ O técnico bloqueou seu teclado e mouse durante a sessão.")
		} else {
			s.chat.append("✓ Seu teclado e mouse foram liberados.")
		}
	}
}
