//go:build windows

package main

import (
	"log"
	"runtime"
	"syscall"
	"unsafe"
)

// chatWindow is a small always-on-top Win32 chat window shown to the person at
// the remote machine: it displays messages from the technician and lets the
// user reply. Pure syscall (no CGO). One per sidecar process. Best-effort: if
// creation fails the rest of the session (video/input/files) is unaffected.

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleH    = kernel32.NewProc("GetModuleHandleW")
	procRegisterClass = user32.NewProc("RegisterClassExW")
	procCreateWindow  = user32.NewProc("CreateWindowExW")
	procDefWindowProc = user32.NewProc("DefWindowProcW")
	procGetMessage    = user32.NewProc("GetMessageW")
	procTranslateMsg  = user32.NewProc("TranslateMessage")
	procDispatchMsg   = user32.NewProc("DispatchMessageW")
	procPostQuit      = user32.NewProc("PostQuitMessage")
	procDestroyWindow = user32.NewProc("DestroyWindow")
	procShowWindow    = user32.NewProc("ShowWindow")
	procSendMessage   = user32.NewProc("SendMessageW")
	procPostMessage   = user32.NewProc("PostMessageW")
	procGetWindowText = user32.NewProc("GetWindowTextW")
	procMoveWindow      = user32.NewProc("MoveWindow")
	procCreateFont      = gdi32.NewProc("CreateFontW")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procSetBkColor      = gdi32.NewProc("SetBkColor")
	procSetTextColor    = gdi32.NewProc("SetTextColor")

	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// Dark theme (COLORREF 0x00BBGGRR) to match a modern remote-tool look.
const (
	clrWinBg  = 0x00181818
	clrCtlBg  = 0x00282828
	clrText   = 0x00EBEBEB
	wmCtlEdit = 0x0133
	wmCtlStatic = 0x0138
)

var bgBrush, ctlBrush uintptr

const (
	wsOverlapped   = 0x00CF0000
	wsVisible      = 0x10000000
	wsChild        = 0x40000000
	wsVScroll      = 0x00200000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsExTopmost    = 0x00000008
	wsExToolwindow = 0x00000080
	cwUseDefault   = 0x80000000
	// Caption + system menu + minimize button (no resize/maximize): a normal,
	// minimizable window that shows in the taskbar so it can be restored.
	wsChatStyle = wsCaption | wsSysMenu | wsMinimizeBox

	dwmaCornerPref = 33 // DWMWA_WINDOW_CORNER_PREFERENCE
	dwmcpRound     = 2  // DWMWCP_ROUND
	dwmaDarkMode   = 20 // DWMWA_USE_IMMERSIVE_DARK_MODE

	esMultiline   = 0x0004
	esAutoVScroll = 0x0040
	esReadonly    = 0x0800

	wmDestroy = 0x0002
	wmClose   = 0x0010
	wmCommand = 0x0111
	wmSetFont = 0x0030
	wmApp     = 0x8000

	emReplaceSel = 0x00C2
	emSetSel     = 0x00B1

	swShow   = 5
	swHide   = 0
	swShowNA = 8 // show in current state without activating (un-hides)

	idHistory = 101
	idInput   = 102
	idSend    = 103
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msgStruct struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

type chatWindow struct {
	hwnd    uintptr
	history uintptr
	input   uintptr
	onSend  func(string)
}

// only one chat window exists per process; the WndProc reaches it here.
var activeChat *chatWindow

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func newChatWindow(title string, onSend func(string)) *chatWindow {
	cw := &chatWindow{onSend: onSend}
	activeChat = cw
	ready := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("chat: window crashed: %v", r)
			}
		}()
		runtime.LockOSThread()
		cw.create(title)
		close(ready)
		cw.loop()
	}()
	<-ready
	return cw
}

func (cw *chatWindow) create(title string) {
	hInst, _, _ := procGetModuleH.Call(0)
	bgBrush, _, _ = procCreateSolidBrush.Call(clrWinBg)
	ctlBrush, _, _ = procCreateSolidBrush.Call(clrCtlBg)
	className := u16("RMMChatWindow")
	wc := wndClassExW{
		style:         0,
		lpfnWndProc:   syscall.NewCallback(wndProc),
		hInstance:     hInst,
		hbrBackground: bgBrush,
		lpszClassName: className,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if atom, _, _ := procRegisterClass.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		log.Printf("chat: RegisterClassEx failed; chat display disabled")
		return
	}
	hwnd, _, _ := procCreateWindow.Call(
		wsExTopmost, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(u16(title))),
		wsChatStyle|wsVisible, cwUseDefault, cwUseDefault, 360, 460, 0, 0, hInst, 0)
	if hwnd == 0 {
		log.Printf("chat: CreateWindowEx failed; chat display disabled")
		return
	}
	cw.hwnd = hwnd
	// Rounded corners + dark title bar (Windows 11 / recent Win10; no-op older).
	pref := int32(dwmcpRound)
	procDwmSetWindowAttribute.Call(hwnd, dwmaCornerPref, uintptr(unsafe.Pointer(&pref)), 4)
	dark := int32(1)
	procDwmSetWindowAttribute.Call(hwnd, dwmaDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
	editClass := u16("EDIT")
	cw.history, _, _ = procCreateWindow.Call(0, uintptr(unsafe.Pointer(editClass)), 0,
		wsChild|wsVisible|wsVScroll|esMultiline|esAutoVScroll|esReadonly, 8, 8, 328, 360, hwnd, idHistory, hInst, 0)
	cw.input, _, _ = procCreateWindow.Call(0, uintptr(unsafe.Pointer(editClass)), 0,
		wsChild|wsVisible, 8, 376, 252, 28, hwnd, idInput, hInst, 0)
	btn, _, _ := procCreateWindow.Call(0, uintptr(unsafe.Pointer(u16("BUTTON"))), uintptr(unsafe.Pointer(u16("Send"))),
		wsChild|wsVisible, 268, 376, 68, 28, hwnd, idSend, hInst, 0)

	// Use a modern UI font instead of the dated default (System/MS Sans Serif).
	font, _, _ := procCreateFont.Call(
		^uintptr(0)-14, 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, // height -15, weight 400, ClearType
		uintptr(unsafe.Pointer(u16("Segoe UI"))))
	for _, h := range []uintptr{cw.history, cw.input, btn} {
		procSendMessage.Call(h, wmSetFont, font, 1)
	}
	procShowWindow.Call(hwnd, swShow)
}

func (cw *chatWindow) loop() {
	if cw.hwnd == 0 {
		return
	}
	var m msgStruct
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) { // WM_QUIT or error
			return
		}
		procTranslateMsg.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMsg.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// append shows a line in the history box. Safe to call from any goroutine
// (SendMessage marshals to the window thread). It also re-reveals the window
// (without stealing focus) so a new message brings back a chat the user closed
// (X just hides it).
func (cw *chatWindow) append(line string) {
	if cw == nil || cw.history == 0 {
		return
	}
	procSendMessage.Call(cw.history, emSetSel, ^uintptr(0), ^uintptr(0))
	procSendMessage.Call(cw.history, emReplaceSel, 0, uintptr(unsafe.Pointer(u16(line+"\r\n"))))
	procShowWindow.Call(cw.hwnd, swShowNA)
}

func (cw *chatWindow) close() {
	if cw == nil || cw.hwnd == 0 {
		return
	}
	procPostMessage.Call(cw.hwnd, wmApp+1, 0, 0)
}

func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	cw := activeChat
	switch msg {
	case wmCtlEdit, wmCtlStatic:
		// Dark-theme the child controls (wparam = HDC).
		procSetBkColor.Call(wparam, clrCtlBg)
		procSetTextColor.Call(wparam, clrText)
		return ctlBrush
	case wmCommand:
		if cw != nil && (wparam&0xffff) == idSend {
			text := getWindowText(cw.input)
			if text != "" {
				if cw.onSend != nil {
					cw.onSend(text)
				}
				cw.append("You: " + text)
				procSendMessage.Call(cw.input, 0x000C, 0, uintptr(unsafe.Pointer(u16("")))) // WM_SETTEXT ""
			}
		}
		return 0
	case wmClose:
		// X just hides the window; the session keeps running.
		procShowWindow.Call(hwnd, swHide)
		return 0
	case wmApp + 1:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuit.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
}

func getWindowText(h uintptr) string {
	var buf [1024]uint16
	n, _, _ := procGetWindowText.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}
