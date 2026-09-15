package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A sidecar that cannot be prepared never reaches the relay, so the viewer has
// only a black canvas to go on. The agent takes its place for one message.
func TestReportRemoteFailureReachesTheViewer(t *testing.T) {
	type received struct {
		auth string
		body []byte
	}
	got := make(chan received, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		got <- received{auth: auth, body: raw}
	}))
	defer server.Close()

	a := &agent{}
	a.reportRemoteFailure("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/remote?role=agent", "ticket-1",
		"this endpoint could not prepare the remote module: no such file")

	select {
	case r := <-got:
		if r.auth != "Remote-Agent ticket-1" {
			t.Fatalf("authorization = %q", r.auth)
		}
		var notice struct{ T, Text string }
		if err := json.Unmarshal(r.body, &notice); err != nil {
			t.Fatalf("notice is not JSON: %v", err)
		}
		if notice.T != "notice" {
			t.Fatalf("type = %q, want notice", notice.T)
		}
		if !strings.Contains(notice.Text, "could not prepare the remote module") {
			t.Fatalf("text = %q", notice.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the viewer never received the failure notice")
	}
}

// Reporting is best effort: an unreachable relay must not panic or block the
// agent, which still has the original failure to log.
func TestReportRemoteFailureToleratesAnUnreachableRelay(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		a := &agent{}
		a.reportRemoteFailure("ws://127.0.0.1:1/ws/remote?role=agent", "", "unreachable")
		a.reportRemoteFailure("", "", "no relay")
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("reporting blocked the agent")
	}
}
