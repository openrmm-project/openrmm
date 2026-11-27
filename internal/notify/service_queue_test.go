package notify

import (
	"context"
	"errors"
	"testing"
)

// With no alerts transport, alert mail is off: nothing is queued. Queuing anyway
// banked stale alerts for the whole retention period and sent them all the day a
// relay was configured. The store is nil here, so reaching it would panic.
func TestQueueSkipsWhenNoTransportIsConfigured(t *testing.T) {
	for name, cfg := range map[string]Config{
		"nothing":            {},
		"sender but no host": {From: "alerts@tenant.test"},
	} {
		s := New(nil, func(context.Context) (Config, string, error) { return cfg, "", nil })
		if err := s.Queue(context.Background(), Event{Type: "alert_opened", AgentID: "a1"}); err != nil {
			t.Errorf("%s: Queue = %v, want nil", name, err)
		}
	}
}

// A settings read error is not off: keep the event for later delivery. A nil
// store demonstrates that the queue path was reached rather than skipped.
func TestQueueStillQueuesWhenSettingsCannotBeRead(t *testing.T) {
	s := New(nil, func(context.Context) (Config, string, error) { return Config{}, "", errors.New("db down") })
	defer func() {
		if recover() == nil {
			t.Error("config read error skipped queuing")
		}
	}()
	_ = s.Queue(context.Background(), Event{Type: "agent_offline", AgentID: "a1"})
}
