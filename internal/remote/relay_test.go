package remote

import "testing"

func TestCloseAgentFencesLateRelayJoin(t *testing.T) {
	r := NewRelay()
	p, err := r.get("session-1", "agent-1")
	if err != nil {
		t.Fatalf("get pair: %v", err)
	}

	r.CloseAgent("agent-1")
	select {
	case <-p.done:
	default:
		t.Fatal("CloseAgent did not wake the pending relay pair")
	}
	if _, err := r.get("session-2", "agent-1"); err == nil {
		t.Fatal("revoked agent was allowed to create a new relay pair")
	}
}
