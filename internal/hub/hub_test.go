package hub

import "testing"

func TestReplacementKeepsCurrentLease(t *testing.T) {
	h := New()
	oldClosed := make(chan struct{})
	old := h.Add("agent-1", func() { close(oldClosed) })
	current := h.Add("agent-1", nil)

	select {
	case <-oldClosed:
	default:
		t.Fatal("replacement did not close the displaced transport")
	}
	if h.Remove("agent-1", old) {
		t.Fatal("displaced lease removed the current connection")
	}
	if !h.IsCurrent("agent-1", current) {
		t.Fatal("replacement is not the current lease")
	}
	if !h.Remove("agent-1", current) {
		t.Fatal("current lease was not removed")
	}
	if h.IsOnline("agent-1") {
		t.Fatal("agent remains online after its current lease closes")
	}
}

func TestDisconnectRemovesLease(t *testing.T) {
	h := New()
	closed := make(chan struct{})
	h.Add("agent-1", func() { close(closed) })

	if !h.Disconnect("agent-1") {
		t.Fatal("Disconnect reported no live connection")
	}
	select {
	case <-closed:
	default:
		t.Fatal("Disconnect did not close the transport")
	}
	if h.IsOnline("agent-1") {
		t.Fatal("agent remains online after Disconnect")
	}
}

func TestDisconnectCurrentDoesNotCloseReplacement(t *testing.T) {
	h := New()
	old := h.Add("agent-1", nil)
	currentClosed := make(chan struct{})
	current := h.Add("agent-1", func() { close(currentClosed) })

	if h.DisconnectCurrent("agent-1", old) {
		t.Fatal("stale lease disconnected the replacement")
	}
	if !h.IsCurrent("agent-1", current) {
		t.Fatal("replacement disappeared after stale disconnect")
	}
	if !h.DisconnectCurrent("agent-1", current) {
		t.Fatal("current lease was not disconnected")
	}
	select {
	case <-currentClosed:
	default:
		t.Fatal("current transport was not closed")
	}
}
