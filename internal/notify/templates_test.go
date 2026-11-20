package notify

import (
	"strings"
	"testing"
)

func TestAlertTemplatesRenderChoicesAndLiteralValues(t *testing.T) {
	d := SampleAlertTemplateData("alert", "https://rmm.example/")
	d.Description, d.Details = "Wiki Zürich", "A literal {{hostname}}\nSecond line <script>"
	c := DefaultAlertTemplates()
	c.Alert.Body = "{{details}}\n{{description}}\n{{hostname}}\n{{agent_id}}\n{{alert_id}}\n{{console_url}}"
	for _, tc := range []struct{ choice, want string }{{"description", "Wiki Zürich"}, {"hostname", "server-01"}, {"both", "Wiki Zürich (server-01)"}} {
		c.DeviceName = tc.choice
		m, err := RenderAlertTemplate(c, "alert", d)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(m.Subject, tc.want) {
			t.Fatalf("subject = %q", m.Subject)
		}
		if !strings.Contains(m.Body, d.Details) || !strings.HasSuffix(m.Body, "https://rmm.example") {
			t.Fatalf("body = %q", m.Body)
		}
	}
	d.Description = " \n "
	c.DeviceName = "description"
	m, err := RenderAlertTemplate(c, "recovery", d)
	if err != nil || m.Subject != "[OpenRMM] Recovered — server-01" {
		t.Fatalf("fallback: %+v, %v", m, err)
	}
	d.Hostname = "host\r\nBcc: other@example.test"
	m, err = RenderAlertTemplate(c, "alert", d)
	if err != nil || strings.ContainsAny(m.Subject, "\r\n") {
		t.Fatalf("header injection: %+v, %v", m, err)
	}
}

func TestAlertTemplatesValidateBothKinds(t *testing.T) {
	for _, mutate := range []func(*AlertTemplates){
		func(c *AlertTemplates) { c.DeviceName = "invalid" },
		func(c *AlertTemplates) { c.Alert.Subject = "hello\nBcc: other@example.test" },
		func(c *AlertTemplates) { c.Recovery.Body = "{{password}}" },
		func(c *AlertTemplates) { c.Alert.Body = "{{if hostname}}" },
		func(c *AlertTemplates) { c.Alert.Body = "{{hostname" },
		func(c *AlertTemplates) { c.Recovery.Subject = strings.Repeat("x", 513) },
		func(c *AlertTemplates) { c.Alert.Body = strings.Repeat("x", 16385) },
		func(c *AlertTemplates) { c.Recovery.Body = "  " },
		func(c *AlertTemplates) { c.Alert.Subject = "bad\x00text" },
	} {
		c := DefaultAlertTemplates()
		mutate(&c)
		if ValidateAlertTemplates(c) == nil {
			t.Fatalf("accepted invalid templates: %+v", c)
		}
	}
	c := DefaultAlertTemplates()
	c.Alert.Subject = "{{ description }}"
	if err := ValidateAlertTemplates(c); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderAlertTemplate(c, "bad", SampleAlertTemplateData("alert", "")); err == nil {
		t.Fatal("accepted invalid event")
	}
}
