package protocol

import (
	"encoding/json"
	"testing"
)

// An agent that reports no user (older than 0.11) sends no field, so the
// server leaves the stored value alone; one that reports nobody logged in
// sends an empty string, which clears it.
func TestHeartbeatLoggedInUserIsOptional(t *testing.T) {
	none, _ := json.Marshal(HeartbeatMsg{})
	if string(none) != `{}` {
		t.Fatalf("no user: %s", none)
	}
	empty := ""
	cleared, _ := json.Marshal(HeartbeatMsg{LoggedInUser: &empty})
	if string(cleared) != `{"logged_in_user":""}` {
		t.Fatalf("nobody logged in: %s", cleared)
	}
	var legacy HeartbeatMsg
	if err := json.Unmarshal([]byte(`{}`), &legacy); err != nil || legacy.LoggedInUser != nil {
		t.Fatalf("legacy heartbeat: %+v %v", legacy, err)
	}
}
