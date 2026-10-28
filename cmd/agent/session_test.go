package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/p1ngu/rmm/internal/protocol"
)

// One whole session against a stand-in server: the agent re-authenticates
// with its stored identity, writes its health marker, runs the job it is sent,
// reports the result, and returns when the server closes the connection.
func TestRunOnceServesASessionEndToEnd(t *testing.T) {
	t.Setenv("RMM_AUTO_UPDATE", "0")
	previous := runJobScript
	runJobScript = func(ctx context.Context, shell, script string) (int, string, string) {
		return 0, "hello from " + shell, ""
	}
	t.Cleanup(func() { runJobScript = previous })

	registered := make(chan protocol.RegisterReq, 1)
	result := make(chan protocol.JobResultMsg, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var env protocol.Envelope
		if conn.ReadJSON(&env) != nil || env.Type != protocol.MsgRegister {
			return
		}
		var req protocol.RegisterReq
		_ = json.Unmarshal(env.Data, &req)
		registered <- req
		data, _ := json.Marshal(protocol.RegisterResp{AgentID: req.AgentID})
		_ = conn.WriteJSON(protocol.Envelope{Type: protocol.MsgRegistered, Data: data})
		job, _ := json.Marshal(protocol.JobMsg{JobID: "job-e2e", Shell: "bash", Script: "echo hi"})
		_ = conn.WriteJSON(protocol.Envelope{Type: protocol.MsgJob, Data: job})
		// Heartbeats and inventory arrive too; wait for the job's result.
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			_ = conn.SetReadDeadline(deadline)
			var frame protocol.Envelope
			if conn.ReadJSON(&frame) != nil {
				return
			}
			if frame.Type == protocol.MsgJobResult {
				var res protocol.JobResultMsg
				_ = json.Unmarshal(frame.Data, &res)
				result <- res
				return // closing the connection ends the session
			}
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	a := newDedupeAgent(t, filepath.Join(dir, "state.json"))
	a.wsURL = "ws" + strings.TrimPrefix(server.URL, "http")
	a.healthPath = filepath.Join(dir, "health", "healthy.json")
	a.st.AgentID, a.st.AgentToken = "agent-7", "stored-token"
	a.statePath = filepath.Join(dir, "state.json")

	errc := make(chan error, 1)
	go func() { errc <- a.runOnce() }()

	select {
	case req := <-registered:
		if req.AgentID != "agent-7" || req.AgentToken != "stored-token" || req.EnrollToken != "" || req.Version != agentVersion {
			t.Fatalf("register = %+v", req)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent never registered")
	}
	select {
	case res := <-result:
		if res.JobID != "job-e2e" || !res.Success || res.Stdout != "hello from bash" {
			t.Fatalf("result = %+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no job result")
	}
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a closed connection must end the session with an error, so the loop reconnects")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runOnce did not return after the server closed")
	}
	marker, err := os.ReadFile(a.healthPath)
	if err != nil || !strings.Contains(string(marker), `"agent_id":"agent-7"`) {
		t.Fatalf("health marker %q, %v", marker, err)
	}
}

// Without a stored identity or an enrollment the long-lived service refuses
// to register rather than enrol itself.
func TestRunOnceWithoutIdentityRefusesToRegister(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	got := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		if err == nil {
			got <- string(raw)
		}
	}))
	defer server.Close()
	a := newDedupeAgent(t, filepath.Join(t.TempDir(), "state.json"))
	a.wsURL = "ws" + strings.TrimPrefix(server.URL, "http")
	err := a.runOnce()
	if err == nil || !strings.Contains(err.Error(), "no identity") {
		t.Fatalf("err = %v", err)
	}
	select {
	case frame := <-got:
		t.Fatalf("sent %s without an identity", frame)
	case <-time.After(200 * time.Millisecond):
	}
}

// A server that refuses the registration ends the session with its reason.
func TestRunOnceReportsARejectedRegistration(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		data, _ := json.Marshal(map[string]string{"error": "device limit reached"})
		_ = conn.WriteJSON(protocol.Envelope{Type: protocol.MsgError, Data: data})
	}))
	defer server.Close()
	a := newDedupeAgent(t, filepath.Join(t.TempDir(), "state.json"))
	a.wsURL = "ws" + strings.TrimPrefix(server.URL, "http")
	a.st.AgentID, a.st.AgentToken = "agent-7", "stored-token"
	if err := a.runOnce(); err == nil || !strings.Contains(err.Error(), "device limit reached") {
		t.Fatalf("err = %v", err)
	}
}
