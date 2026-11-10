package models

import (
	"testing"

	"github.com/p1ngu/rmm/internal/protocol"
)

// The server and the agent name the device-query job type separately (models
// is not imported by the agent); they must agree.
func TestDeviceQueryJobTypeMatchesTheProtocol(t *testing.T) {
	if JobDeviceQuery != protocol.JobTypeDeviceQuery {
		t.Fatalf("models %q, protocol %q", JobDeviceQuery, protocol.JobTypeDeviceQuery)
	}
}

func TestScriptReleaseHashBindsVariableDeclarations(t *testing.T) {
	old := ScriptContentHash("sh", "echo", nil)
	if old != ScriptContentHash("sh", "echo", nil, []string{}) {
		t.Fatal("legacy release hashes changed")
	}
	sc := &Script{Shell: "sh", Body: "echo", Variables: []string{"CUSTOM_KEY"}}
	sc.AIReleasedHash = ScriptContentHash(sc.Shell, sc.Body, sc.Params, sc.Variables)
	if !sc.AIReleaseCurrent() {
		t.Fatal("control release invalid")
	}
	sc.Variables = []string{"OTHER_KEY"}
	if sc.AIReleaseCurrent() {
		t.Fatal("variable change did not invalidate release")
	}
	if old == sc.AIReleasedHash {
		t.Fatal("variables omitted from hash")
	}
}
