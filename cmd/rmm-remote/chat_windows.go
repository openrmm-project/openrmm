//go:build windows

package main

import (
	"log"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// chatWindow is a small always-on-top Win32 chat window shown to the person at
// the remote machine: it displays messages from the technician and lets the
// user reply. Pure syscall (no CGO). One per sidecar process. Best-effort: if
// creation fails the rest of the session (video/input/files) is unaffected.

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleH       = kernel32.NewProc("GetModuleHandleW")
	procRegisterClass    = user32.NewProc("RegisterClassExW")
	procCreateWindow     = user32.NewProc("CreateWindowExW")
	procDefWindowProc    = user32.NewProc("DefWindowProcW")
	procGetMessage       = user32.NewProc("GetMessageW")
	procTranslateMsg     = user32.NewProc("TranslateMessage")
	procDispatchMsg      = user32.NewProc("DispatchMessageW")
	procPostQuit         = user32.NewProc("PostQuitMessage")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSendMessage      = user32.NewProc("SendMessageW")
	procPostMessage      = user32.NewProc("PostMessageW")
	procGetWindowText    = user32.NewProc("GetWindowTextW")
	procMoveWindow       = user32.NewProc("MoveWindow")
	procCreateFont       = gdi32.NewProc("CreateFontW")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procSetBkColor       = gdi32.NewProc("SetBkColor")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProc   = user32.NewProc("CallWindowProcW")
	procLoadImage        = user32.NewProc("LoadImageW")

	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// Dark theme (COLORREF 0x00BBGGRR) to match a modern remote-tool look.
const (
	clrWinBg    = 0x00181818
	clrCtlBg    = 0x00282828
	clrText     = 0x00EBEBEB
	wmCtlEdit   = 0x0133
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
	wmKeyDown = 0x0100
	wmChar    = 0x0102
	wmApp     = 0x8000

	vkReturn    = 0x0D
	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC (-4) for SetWindowLongPtr

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
	self       string // how the person here is named (selfLabel)
	hwnd       uintptr
	history    uintptr
	input      uintptr
	onSend     func(string)
	mu         sync.Mutex
	pending    []string
	textBuffer [4096]uint16 // heap-owned: Win32 callbacks can grow the Go stack
}

// only one chat window exists per process; the WndProc reaches it here.
var activeChat *chatWindow

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func newChatWindow(title string, onSend func(string)) *chatWindow {
	cw := &chatWindow{onSend: onSend, self: selfLabel(processAccount())}
	activeChat = cw
	ready := make(chan struct{})
	go func() {
		var readyOnce sync.Once
		defer readyOnce.Do(func() { close(ready) })
		defer func() {
			if r := recover(); r != nil {
				log.Printf("chat: window crashed: %v", r)
			}
		}()
		runtime.LockOSThread()
		cw.create(title)
		readyOnce.Do(func() { close(ready) })
		cw.loop()
	}()
	<-ready
	return cw
}

const (
	appIconID  = 1 // the icon group rsrc_windows_*.syso embeds (make winres)
	imageIcon  = 1 // IMAGE_ICON
	smCxIcon   = 11
	smCxSmIcon = 49
)

// appIcon loads the embedded OpenRMM icon at the size the shell asks for
// (title bar and taskbar use different ones). 0 — no icon in this build —
// leaves Windows' default, as before.
func appIcon(hInst uintptr, metric uintptr) uintptr {
	size, _, _ := procGetSystemMetrics.Call(metric)
	h, _, _ := procLoadImage.Call(hInst, appIconID, imageIcon, size, size, 0)
	return h
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
		hIcon:         appIcon(hInst, smCxIcon),
		hIconSm:       appIcon(hInst, smCxSmIcon),
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
	if err := syscall.NewLazyDLL("Msftedit.dll").Load(); err != nil {
		log.Printf("chat: RichEdit unavailable: %v", err)
		procDestroyWindow.Call(hwnd)
		cw.hwnd = 0
		return
	}
	cw.history, _, _ = procCreateWindow.Call(0, uintptr(unsafe.Pointer(u16("RICHEDIT50W"))), 0,
		wsChild|wsVisible|wsVScroll|esMultiline|esAutoVScroll|esReadonly, 8, 8, 328, 360, hwnd, idHistory, hInst, 0)
	cw.input, _, _ = procCreateWindow.Call(0, uintptr(unsafe.Pointer(editClass)), 0,
		wsChild|wsVisible|0x0080|0x00010000, 8, 376, 252, 28, hwnd, idInput, hInst, 0)
	btn, _, _ := procCreateWindow.Call(0, uintptr(unsafe.Pointer(u16("BUTTON"))), uintptr(unsafe.Pointer(u16("Send"))),
		wsChild|wsVisible, 268, 376, 68, 28, hwnd, idSend, hInst, 0)
	// Subclass the input box so Enter submits (a single-line EDIT ignores it by
	// default). We forward Enter as a Send button click and swallow the keystroke.
	newEditProc := syscall.NewCallback(editSubclassProc)
	op, _, _ := procSetWindowLongPtr.Call(cw.input, gwlpWndProc, newEditProc)
	origEditProc = op

	// Use a modern UI font instead of the dated default (System/MS Sans Serif).
	font, _, _ := procCreateFont.Call(
		^uintptr(0)-14, 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, // height -15, weight 400, ClearType
		uintptr(unsafe.Pointer(u16("Segoe UI"))))
	for _, h := range []uintptr{cw.history, cw.input, btn} {
		procSendMessage.Call(h, wmSetFont, font, 1)
	}
	procSendMessage.Call(cw.history, 0x0443, 0, clrCtlBg) // EM_SETBKGNDCOLOR
	procShowWindow.Call(hwnd, swShow)
}

// origEditProc holds the input EDIT's original window procedure so the subclass
// can defer non-Enter messages to it.
var origEditProc uintptr

// editSubclassProc intercepts Enter in the input box and turns it into a Send
// click; everything else falls through to the stock EDIT behavior.
func editSubclassProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wmKeyDown && wparam == vkReturn {
		if activeChat != nil {
			procSendMessage.Call(activeChat.hwnd, wmCommand, idSend, 0)
		}
		return 0
	}
	if msg == wmChar && wparam == vkReturn { // swallow the CR so the edit doesn't beep
		return 0
	}
	r, _, _ := procCallWindowProc.Call(origEditProc, hwnd, msg, wparam, lparam)
	return r
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
	cw.mu.Lock()
	cw.pending = append(cw.pending, line)
	cw.mu.Unlock()
	procPostMessage.Call(cw.hwnd, wmApp+2, 0, 0)
}

// CHARFORMATW, not CHARFORMAT2: explicit layout is identical on amd64/arm64.
type chatCharFormat struct {
	Size, Mask, Effects uint32
	Height, Offset      int32
	Color               uint32
	Charset, Pitch      byte
	Face                [32]uint16
}

func (cw *chatWindow) flushMessages() {
	cw.mu.Lock()
	lines := cw.pending
	cw.pending = nil
	cw.mu.Unlock()
	for _, line := range lines {
		color := uint32(clrText)
		if strings.HasPrefix(line, "Tech: ") {
			color = 0x00FFB5D8
			line = "Technician: " + strings.TrimPrefix(line, "Tech: ")
		}
		if strings.HasPrefix(line, "You: ") {
			color = 0x00B5E8A4
			line = cw.self + ": " + strings.TrimPrefix(line, "You: ")
		}
		if strings.HasPrefix(line, chatGuestPrefix) {
			color = 0x006BC2F5 // amber: a guest, neither the technician nor the person here
			line = strings.TrimPrefix(line, chatGuestPrefix)
		}
		format := chatCharFormat{Mask: 0x40000000, Color: color} // CFM_COLOR
		format.Size = uint32(unsafe.Sizeof(format))
		procSendMessage.Call(cw.history, emSetSel, ^uintptr(0), ^uintptr(0))
		procSendMessage.Call(cw.history, 0x0444, 1, uintptr(unsafe.Pointer(&format))) // EM_SETCHARFORMAT, SCF_SELECTION
		procSendMessage.Call(cw.history, emReplaceSel, 0, uintptr(unsafe.Pointer(u16(line+"\r\n"))))
	}
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
		if cw != nil && (wparam&0xffff) == idSend && wparam>>16 == 0 {
			text := getWindowText(cw.input)
			if text != "" {
				if cw.onSend != nil {
					go cw.onSend(text) // never block the Win32 UI/relay reader
				}
				cw.append("You: " + text)
				procSendMessage.Call(cw.input, 0x000C, 0, uintptr(unsafe.Pointer(u16("")))) // WM_SETTEXT ""
			}
		}
		return 0
	case wmApp + 2:
		if cw != nil {
			cw.flushMessages()
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
	if activeChat == nil {
		return ""
	}
	buf := activeChat.textBuffer[:]
	n, _, _ := procSendMessage.Call(h, 0x000D, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0]))) // WM_GETTEXT
	if n >= uintptr(len(buf)) {
		return ""
	}
	return strings.TrimSpace(syscall.UTF16ToString(buf[:n]))
}
