//go:build windows

package main

import (
	"github.com/gorilla/websocket"
	"testing"
	"time"
	"unsafe"
)

func TestChatUnicodeReply(t *testing.T) {
	replies := make(chan string, 2)
	cw := newChatWindow("OpenRMM chat regression test", func(text string) { replies <- text })
	defer cw.close()
	if cw.hwnd == 0 || cw.input == 0 || cw.history == 0 {
		t.Fatal("native chat controls failed to initialize")
	}
	for _, want := range []string{"Olá, técnico! Ação e resolução — 日本語", "Segunda resposta"} {
		procSendMessage.Call(cw.input, 0x000C, 0, uintptr(unsafe.Pointer(u16(want))))
		procSendMessage.Call(cw.input, wmKeyDown, vkReturn, 0)
		select {
		case got := <-replies:
			if got != want {
				t.Fatalf("reply = %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("native chat reply timed out")
		}
	}
}

func TestFilesOnlyRejectsDesktopControls(t *testing.T) {
	s := &session{out: make(chan wsMsg, 8), done: make(chan struct{})}
	for _, command := range []string{"cad", "uac", "lockinput", "chat", "mon"} {
		s.onMessage(websocket.TextMessage, []byte(`{"t":"`+command+`","enable":true,"text":"test"}`))
	}
	if len(s.out) != 0 {
		t.Fatal("files-only session processed a desktop command")
	}
}
