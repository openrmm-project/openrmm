package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestTerminalDialsConfiguredRelay(t *testing.T) {
	tests := []struct {
		name  string
		start protocol.RemoteStartMsg
		uri   string
		auth  string
	}{
		{
			name: "agent ticket", start: protocol.RemoteStartMsg{SessionID: "s", AgentTicket: "agent-ticket"},
			uri: "/ws/remote?role=agent&session=s", auth: "Remote-Agent agent-ticket",
		},
		{
			name: "legacy token", start: protocol.RemoteStartMsg{SessionID: "s", Token: "legacy+t"},
			uri: "/ws/remote?role=agent&token=legacy%2Bt",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Record the handshake and decline it so no shell starts.
				requests <- r
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()

			origin := "ws" + strings.TrimPrefix(server.URL, "http")
			a := &agent{wsURL: origin + "/ws/agent"}
			test.start.RelayURL = origin + "/another-view"
			a.startTerminalSession(test.start)

			select {
			case req := <-requests:
				if uri := req.URL.RequestURI(); uri != test.uri {
					t.Errorf("request URI = %q, want %q", uri, test.uri)
				}
				if auth := req.Header.Get("Authorization"); auth != test.auth {
					t.Errorf("authorization = %q, want %q", auth, test.auth)
				}
			default:
				t.Fatal("terminal did not reach the configured relay")
			}
		})
	}
}
