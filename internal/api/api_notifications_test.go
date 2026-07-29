package api

import (
	"testing"

	"github.com/p1ngu/rmm/internal/notify"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// An address reaches notify.Send, which writes it straight into a To: header.
// Rejecting the CR/LF forms here keeps that injection out of the outbox rather
// than relying on the renderer's sanitiser alone.
func TestValidEmail(t *testing.T) {
	tests := []struct {
		name, addr string
		want       bool
	}{
		{"plain", "ops@example.test", true},
		{"subaddress", "ops+alerts@example.test", true},
		{"empty", "", false},
		{"no domain", "ops", false},
		{"two at signs", "ops@a@example.test", false},
		{"display name form", "Ops <ops@example.test>", false},
		{"header injection", "ops@example.test\r\nBcc: attacker@evil.test", false},
		{"embedded space", "ops @example.test", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validEmail(tt.addr); got != tt.want {
				t.Fatalf("validEmail(%q) = %t, want %t", tt.addr, got, tt.want)
			}
		})
	}
}

// The preferences screen must show every event type with a meaningful state,
// including the ones the user has never touched.
func TestNotificationPrefsMergesDefaults(t *testing.T) {
	prefs := notificationPrefs(map[string]bool{store.NotifyCheckFailed: false})
	if len(prefs) != len(store.NotificationEventTypes) {
		t.Fatalf("got %d prefs, want %d", len(prefs), len(store.NotificationEventTypes))
	}
	byType := map[string]map[string]any{}
	for _, p := range prefs {
		byType[p["event_type"].(string)] = p
	}

	opted := byType[store.NotifyCheckFailed]
	if opted["enabled"].(bool) || !opted["explicit"].(bool) {
		t.Error("an explicit opt-out should be reported as disabled and explicit")
	}
	if !opted["default"].(bool) {
		t.Error("check_failed should still report its default-on baseline")
	}

	untouched := byType[store.NotifyAgentOffline]
	if !untouched["enabled"].(bool) || untouched["explicit"].(bool) {
		t.Error("an untouched default-on event should be enabled and not explicit")
	}
	// Failed jobs fan out across the fleet, so silence is the default.
	if byType[store.NotifyJobFailed]["enabled"].(bool) {
		t.Error("job_failed should default to off")
	}
}

// The password is write-only: the console sets it and never reads it back, so a
// stolen session cannot use this endpoint to recover the mailbox credential.
func TestSMTPViewHidesPassword(t *testing.T) {
	view := smtpView(notify.Config{
		Host: "smtp.example.test", Port: 587, From: "rmm@example.test", Password: "s3cret",
	})
	for key, value := range view {
		if s, ok := value.(string); ok && s == "s3cret" {
			t.Fatalf("smtpView exposed the password in %q", key)
		}
	}
	if view["password_set"] != true {
		t.Error("password_set should report that a password is stored")
	}
	if view["enabled"] != true {
		t.Error("a host and sender should make the transport enabled")
	}
}

// Auditing which fields were touched is the point; auditing their values would
// copy the mail credentials into a table meant for a trail.
func TestChangedSMTPRecordsFieldsNotValues(t *testing.T) {
	changed := changedSMTP(settings.SMTPSaveRequest{Host: "smtp.example.test", Password: "s3cret"})
	var sawPassword bool
	for _, field := range changed {
		if field == "smtp.password" {
			sawPassword = true
		}
		if field == "s3cret" {
			t.Fatal("changedSMTP leaked a secret value into the audit detail")
		}
	}
	if !sawPassword {
		t.Error("a submitted password should be recorded as a changed field")
	}

	cleared := changedSMTP(settings.SMTPSaveRequest{})
	if len(cleared) != 1 || cleared[0] != "smtp.cleared" {
		t.Errorf("clearing the host should audit as smtp.cleared, got %v", cleared)
	}
}
