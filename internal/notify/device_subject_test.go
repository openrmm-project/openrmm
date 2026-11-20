package notify

import (
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/store"
)

func TestAlertSubjectsPreferDescription(t *testing.T) {
	for _, tc := range []struct{ name, description, hostname, want string }{
		{"description", "srv12-Wiki.js", "technical-host", "srv12-Wiki.js"},
		{"empty", "", "technical-host", "technical-host"},
		{"whitespace", " \t\r\n ", "technical-host", "technical-host"},
		{"unicode", "  Serveur Zürich  ", "technical-host", "Serveur Zürich"},
		{"multiline", "Serveur\r\nWiki", "technical-host", "Serveur Wiki"},
		{"missing hostname", "", "", "agent-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, eventType := range []string{store.NotifyCheckFailed, store.NotifyAgentOffline, store.NotifyCheckRecovered, store.NotifyAgentRecovered} {
				build := AlertOpenedEvent
				prefix := "[OpenRMM] " + labelFor(eventType) + " — "
				if eventType == store.NotifyCheckRecovered || eventType == store.NotifyAgentRecovered {
					build = AlertResolvedEvent
					prefix = "[OpenRMM] Recovered — "
				}
				ev := build(eventType, "agent-id", tc.hostname, tc.description, "alert-id", "detail", "https://console.example")
				if ev.Subject != prefix+tc.want {
					t.Errorf("%s: subject = %q, want %q", eventType, ev.Subject, prefix+tc.want)
				}
				if ev.Hostname != tc.hostname || !strings.Contains(ev.Body, "Device: "+tc.hostname+"\n") {
					t.Errorf("technical hostname was changed: %+v", ev)
				}
			}
		})
	}
}
