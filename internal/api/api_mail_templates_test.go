package api

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/notify"
	"github.com/p1ngu/rmm/internal/notify/smtptest"
)

func TestAlertTemplatesConfigurationIntegration(t *testing.T) {
	e := newSMTPE2E(t, func(c *config.Config) {
		c.PublicURL = "https://agents.example.test"
		c.ConsolePublicURL = "https://console.example.test"
	})
	code, view := e.do(http.MethodGet, "/api/config/mail/templates", "", e.admin)
	if code != 200 || view["config"].(map[string]any)["device_name"] != "hostname" {
		t.Fatalf("defaults: %d %+v", code, view)
	}
	c := notify.DefaultAlertTemplates()
	c.DeviceName = "both"
	c.Alert.Subject, c.Alert.Body = "Incident {{device}}", "Détails : {{details}}\n{{console_url}}"
	preview, _ := json.Marshal(map[string]any{"config": c, "event": "alert"})
	code, view = e.do(http.MethodPost, "/api/config/mail/templates/preview", string(preview), e.admin)
	if code != 200 || view["subject"] != "Incident Wiki server (server-01)" {
		t.Fatalf("preview: %d %+v", code, view)
	}
	if !strings.Contains(view["body"].(string), "https://console.example.test") || strings.Contains(view["body"].(string), "https://agents.example.test") {
		t.Fatalf("preview console link: %+v", view)
	}
	if got, err := notify.LoadAlertTemplates(e.ctx, e.store); err != nil || got.DeviceName != "hostname" {
		t.Fatal("preview persisted changes")
	}
	body, _ := json.Marshal(c)
	if code, view = e.do(http.MethodPut, "/api/config/mail/templates", string(body), e.admin); code != 200 {
		t.Fatalf("save: %d %+v", code, view)
	}
	if got, err := notify.LoadAlertTemplates(e.ctx, e.store); err != nil || got != c {
		t.Fatalf("stored: %+v %v", got, err)
	}
	c.Recovery.Body = "{{unknown}}"
	body, _ = json.Marshal(c)
	if code, _ = e.do(http.MethodPut, "/api/config/mail/templates", string(body), e.admin); code != 400 {
		t.Fatalf("invalid template: %d", code)
	}
	got, _ := notify.LoadAlertTemplates(e.ctx, e.store)
	if got.Recovery.Body == c.Recovery.Body {
		t.Fatal("invalid save overwrote settings")
	}
	if _, err := e.store.GetAppSetting(e.ctx, "smtp.alerts.host"); err == nil {
		t.Fatal("template save altered SMTP settings")
	}
}

func TestAlertTemplatesRequireGlobalSettingsPermissionIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	client, err := e.store.CreateClient(e.ctx, "template-client")
	if err != nil {
		t.Fatal(err)
	}
	scoped := e.session(models.RoleAdmin, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	tech := e.session(models.RoleTech, nil)
	for _, token := range []string{tech, scoped, ""} {
		for _, route := range []struct{ method, path string }{{"GET", "/api/config/mail/templates"}, {"PUT", "/api/config/mail/templates"}, {"POST", "/api/config/mail/templates/preview"}} {
			code, _ := e.do(route.method, route.path, `{}`, token)
			if code != 401 && code != 403 {
				t.Fatalf("%s %s: unauthorized status %d", route.method, route.path, code)
			}
		}
	}
}

func TestAlertTemplatesSendStoredExampleIntegration(t *testing.T) {
	relay := smtptest.New(t)
	e := newSMTPE2E(t, func(c *config.Config) { c.SMTPAlertsAllowPrivate = true })
	relayBody := fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"from":"alerts@example.test","tls_mode":"none"}`, relay.Port)
	if code, out := e.do("PUT", "/api/config/smtp/alerts", relayBody, e.admin); code != 200 {
		t.Fatalf("relay: %d %+v", code, out)
	}
	user, err := e.store.CreateUser(e.ctx, "template-operator", "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.store.UpdateUserEmail(e.ctx, user.ID, "operator@example.test"); err != nil {
		t.Fatal(err)
	}
	session := e.sessionFor(user.ID)
	c := notify.DefaultAlertTemplates()
	c.DeviceName = "description"
	c.Recovery.Subject, c.Recovery.Body = "Restored {{device}}", "{{description}}: {{details}}"
	body, _ := json.Marshal(c)
	if code, out := e.do("PUT", "/api/config/mail/templates", string(body), session); code != 200 {
		t.Fatalf("save: %d %+v", code, out)
	}
	if code, out := e.do("POST", "/api/config/smtp/alerts/test", `{"template_event":"recovery"}`, session); code != 200 {
		t.Fatalf("send: %d %+v", code, out)
	}
	messages := relay.Messages()
	if len(messages) != 1 || messages[0].To[0] != "operator@example.test" {
		t.Fatalf("delivery: %+v", messages)
	}
	message, err := mail.ReadMessage(strings.NewReader(messages[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || subject != "[TEST] Restored Wiki server" {
		t.Fatalf("subject: %q %v", subject, err)
	}
	rendered, _ := io.ReadAll(message.Body)
	if !strings.Contains(string(rendered), "Wiki server: Example only:") {
		t.Fatalf("body: %s", rendered)
	}
	if code, _ := e.do("POST", "/api/config/smtp/alerts/test", `{"template_event":"unknown"}`, session); code != 400 {
		t.Fatalf("invalid kind: %d", code)
	}
	if len(relay.Messages()) != 1 {
		t.Fatal("invalid event sent a message")
	}
}
