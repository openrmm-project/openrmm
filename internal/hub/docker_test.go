package hub

import "testing"

func TestDockerCapabilityBelongsToConnection(t *testing.T) {
	h := New()
	old := h.Add("agent", nil)
	if h.SupportsDocker("agent") || h.SendDockerCheck("agent", []byte("check")) {
		t.Fatal("legacy connection accepted new check")
	}
	if !h.SetDockerSupport("agent", old, true) || !h.SendDockerCheck("agent", []byte("check")) {
		t.Fatal("capable connection not enabled")
	}
	<-old
	current := h.Add("agent", nil)
	if h.SupportsDocker("agent") || h.SendDockerCheck("agent", []byte("check")) {
		t.Fatal("replacement inherited capability")
	}
	if h.SetDockerSupport("agent", old, true) {
		t.Fatal("displaced lease changed capability")
	}
	if !h.SetDockerSupport("agent", current, true) {
		t.Fatal("current lease refused")
	}
	h.SetDockerSupport("agent", current, false)
	if h.SupportsDocker("agent") {
		t.Fatal("disabled collection retained capability")
	}
	h.Remove("agent", current)
	if h.SupportsDocker("agent") {
		t.Fatal("offline retained capability")
	}
}
