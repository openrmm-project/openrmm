package remote

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCloseAgentFencesLateRelayJoin(t *testing.T) {
	r := NewRelay()
	p, err := r.get("session-1", "agent-1")
	if err != nil {
		t.Fatalf("get pair: %v", err)
	}

	r.CloseAgent("agent-1")
	select {
	case <-p.done:
	default:
		t.Fatal("CloseAgent did not wake the pending relay pair")
	}
	if _, err := r.get("session-2", "agent-1"); err == nil {
		t.Fatal("revoked agent was allowed to create a new relay pair")
	}
}

func TestCloseSessionTellsTheViewerWhy(t *testing.T) {
	r := NewRelay()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		_ = r.Join("session-1", "agent-1", req.URL.Query().Get("role"), conn)
	}))
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	agent, _, err := websocket.DefaultDialer.Dial(base+"?role=agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	viewer, _, err := websocket.DefaultDialer.Dial(base+"?role=viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()

	// Wait for the pair to form, then end it the way a replacement does.
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		p := r.pairs["session-1"]
		r.mu.Unlock()
		if p != nil {
			p.mu.Lock()
			both := p.agent != nil && p.viewer != nil
			p.mu.Unlock()
			if both {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the pair never formed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.CloseSession("session-1", CloseSessionReplaced, "remote_session_replaced")

	_ = viewer.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = viewer.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != CloseSessionReplaced {
		t.Fatalf("the viewer must learn it was replaced, or its reconnect loop takes the session back; got %v", err)
	}
}
