package api

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/store"
)

func TestInventoryMissingSnapshotsAreLogged(t *testing.T) {
	s := &Server{hub: hub.New()}
	lease := s.hub.Add("agent-1", nil)
	defer s.hub.Remove("agent-1", lease)
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	for _, frame := range []string{
		`{"type":"inventory","data":null}`,
		`{"type":"inventory","data":{}}`,
		`{"type":"inventory","data":{"data":null}}`,
		`{"type":"inventory","data":"private-inventory-value"}`,
	} {
		logs.Reset()
		s.handleAgentFrame(context.Background(), "agent-1", lease, []byte(frame))
		if !strings.Contains(logs.String(), "inventory: invalid snapshot agent=agent-1") {
			t.Fatalf("missing diagnostic: %s", logs.String())
		}
		if strings.Contains(logs.String(), "private-inventory-value") {
			t.Fatal("inventory contents leaked into logs")
		}
	}
}

func TestInventoryInvalidSnapshotsDoNotReachDatabase(t *testing.T) {
	// No pool: if an invalid snapshot reaches SQL, the test will panic.
	s := &Server{hub: hub.New(), store: &store.Store{}}
	lease := s.hub.Add("agent-1", nil)
	defer s.hub.Remove("agent-1", lease)
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	for _, frame := range []string{
		`{"type":"inventory","data":{"data":[]}}`,
		`{"type":"inventory","data":{"data":"private-inventory-value"}}`,
		`{"type":"inventory","data":{"data":{"private-inventory-value\u0000":1}}}`,
	} {
		logs.Reset()
		s.handleAgentFrame(context.Background(), "agent-1", lease, []byte(frame))
		if !strings.Contains(logs.String(), "inventory: persistence failed agent=agent-1") || strings.Contains(logs.String(), "snapshot stored") {
			t.Fatalf("incorrect diagnostic: %s", logs.String())
		}
		if strings.Contains(logs.String(), "private-inventory-value") {
			t.Fatal("inventory contents leaked into logs")
		}
	}
}
