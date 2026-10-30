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
