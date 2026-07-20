package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestEnrollOncePersistsIdentity(t *testing.T) {
	registered := make(chan protocol.RegisterReq, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var env protocol.Envelope
		if json.Unmarshal(raw, &env) != nil || env.Type != protocol.MsgRegister {
			return
		}
		var req protocol.RegisterReq
		if json.Unmarshal(env.Data, &req) != nil {
			return
		}
		registered <- req
		data, _ := json.Marshal(protocol.RegisterResp{AgentID: "agent-1", AgentToken: "agent-token"})
		_ = conn.WriteJSON(protocol.Envelope{Type: protocol.MsgRegistered, Data: data})
	}))
	defer server.Close()

	a := &agent{
		wsURL:           "ws" + strings.TrimPrefix(server.URL, "http"),
		enrollToken:     "enroll-token",
		allowEnrollment: true,
		statePath:       filepath.Join(t.TempDir(), "state.json"),
	}
	if err := a.enrollOnce(); err != nil {
		t.Fatalf("enrollOnce: %v", err)
	}
	if a.st.AgentID != "agent-1" || a.st.AgentToken != "agent-token" {
		t.Fatalf("state = %+v", a.st)
	}
	select {
	case req := <-registered:
		if req.EnrollToken != "enroll-token" {
			t.Fatalf("enroll token = %q", req.EnrollToken)
		}
	default:
		t.Fatal("server did not receive registration")
	}
}
