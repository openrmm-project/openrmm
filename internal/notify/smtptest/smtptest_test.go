package smtptest

import (
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/notify"
)

// The fake server has to speak enough SMTP for the real client, or every test
// built on it would fail for the wrong reason.
func TestServerAcceptsWhatTheClientSends(t *testing.T) {
	srv := New(t)
	cfg := notify.Config{Host: "127.0.0.1", Port: srv.Port, From: "alerts@notify.test", FromName: "Alerts", TLSMode: "none"}
	if err := notify.Send(cfg, notify.Message{To: "ana@example.test", Subject: "Hi", Body: "line one\n.line with a dot\n"}); err != nil {
		t.Fatal(err)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 || msgs[0].From != "alerts@notify.test" || msgs[0].To[0] != "ana@example.test" {
		t.Fatalf("messages = %+v", msgs)
	}
	if !strings.Contains(msgs[0].Data, ".line with a dot") || !strings.Contains(msgs[0].Data, "Subject: Hi") {
		t.Fatalf("body not intact: %q", msgs[0].Data)
	}
}
