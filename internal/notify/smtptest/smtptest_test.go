package smtptest

import (
	"mime"
	"net/mail"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/notify"
)

// The fake server has to speak enough SMTP for the real client, or every test
// built on it would fail for the wrong reason.
func TestServerAcceptsWhatTheClientSends(t *testing.T) {
	srv := New(t)
	cfg := notify.Config{Host: "127.0.0.1", Port: srv.Port, From: "alerts@notify.test", FromName: "Operações", TLSMode: "none"}
	const link = "https://example.test/verify?ref=" + "123456789012345678901234567890123456789012345678901234567890"
	msg := notify.Message{
		To:      "Ana <ana@example.test>",
		Subject: "Status\r\ndo serviço",
		Body:    "Olá, Ana.\r.line with a dot\r\nConfira:\n" + link,
	}
	if err := notify.Send(cfg, msg); err != nil {
		t.Fatal(err)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("received %d messages, want one", len(msgs))
	}
	got := msgs[0]
	if got.From != "alerts@notify.test" || len(got.To) != 1 || got.To[0] != "ana@example.test" {
		t.Fatalf("SMTP envelope from %q to %v", got.From, got.To)
	}
	wire, err := mail.ReadMessage(strings.NewReader(got.Data))
	if err != nil {
		t.Fatal(err)
	}
	if encoding := wire.Header.Get("Content-Transfer-Encoding"); encoding != "quoted-printable" {
		t.Errorf("message encoding = %q", encoding)
	}
	from, err := mail.ParseAddress(wire.Header.Get("From"))
	if err != nil || from.Name != "Operações" || from.Address != got.From {
		t.Errorf("From header = %v, %v", from, err)
	}
	to, err := mail.ParseAddress(wire.Header.Get("To"))
	if err != nil || to.Name != "Ana" || to.Address != got.To[0] {
		t.Errorf("To header = %v, %v", to, err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(wire.Header.Get("Subject"))
	if err != nil || subject != "Status do serviço" || len(wire.Header["Subject"]) != 1 {
		t.Errorf("Subject header = %q, %v", subject, err)
	}
	if body, want := got.Text(t), "Olá, Ana.\r\n.line with a dot\r\nConfira:\r\n"+link+"\r\n"; body != want {
		t.Errorf("decoded body = %q, want %q", body, want)
	}
}
