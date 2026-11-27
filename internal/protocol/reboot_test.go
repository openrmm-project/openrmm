package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHeartbeatRebootOptionalAndExplicit(t *testing.T) {
	for _, raw := range []string{`{}`, `{"logged_in_user":""}`, `{"reboot":null}`} {
		var hb HeartbeatMsg
		if err := json.Unmarshal([]byte(raw), &hb); err != nil || hb.Reboot != nil {
			t.Fatalf("legacy %s: %+v %v", raw, hb, err)
		}
	}
	for _, state := range []string{RebootRequired, RebootNotRequired, RebootUnknown} {
		input := HeartbeatMsg{Reboot: &RebootStatus{Status: state}}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var output HeartbeatMsg
		if err = json.Unmarshal(raw, &output); err != nil || output.Reboot == nil || output.Reboot.Status != state || !output.Reboot.Valid() {
			t.Fatalf("roundtrip %s: %+v %v", raw, output, err)
		}
	}
	for _, r := range []RebootStatus{{Status: ""}, {Status: "yes"}, {Status: RebootRequired, Reason: strings.Repeat("x", 2049)}, {Status: RebootUnknown, BootID: strings.Repeat("x", 129)}} {
		if r.Valid() {
			t.Fatalf("accepted malformed report: %+v", r)
		}
	}
	var hb HeartbeatMsg
	if err := json.Unmarshal([]byte(`{"reboot":false}`), &hb); err == nil {
		t.Fatal("accepted wrong heartbeat shape")
	}
	r := (RebootStatus{Status: RebootRequired, Reason: "kernel\x00\r\nupdate", BootID: " boot-one\x00\n"}).Clean()
	if r.Reason != "kernel update" || r.BootID != "boot-one" {
		t.Fatalf("control characters survived: %+v", r)
	}
}
