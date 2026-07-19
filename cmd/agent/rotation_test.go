package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestRotateCredentialPersistsPendingIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a := &agent{statePath: path, st: state{AgentID: "agent-1", AgentToken: "old"}}

	a.rotateCredential(protocol.CredentialRotateMsg{RotationID: "rotation-1", AgentToken: "new"})

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var got state
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if got.AgentToken != "new" || got.PendingRotationID != "rotation-1" {
		t.Fatalf("state = %+v, want persisted replacement", got)
	}

	a.rotateCredential(protocol.CredentialRotateMsg{RotationID: "rotation-2", AgentToken: "other"})
	if a.st.AgentToken != "new" || a.st.PendingRotationID != "rotation-1" {
		t.Fatalf("second rotation overwrote pending state: %+v", a.st)
	}
}

func TestRotateCredentialAcknowledgesPersistence(t *testing.T) {
	a := &agent{
		statePath: filepath.Join(t.TempDir(), "state.json"),
		st:        state{AgentID: "agent-1", AgentToken: "old"},
		send:      make(chan []byte, 1),
	}
	a.rotateCredential(protocol.CredentialRotateMsg{RotationID: "rotation-1", AgentToken: "new"})

	select {
	case raw := <-a.send:
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode acknowledgement: %v", err)
		}
		if env.Type != protocol.MsgCredentialRotated {
			t.Fatalf("message type = %q, want %q", env.Type, protocol.MsgCredentialRotated)
		}
	default:
		t.Fatal("rotation did not acknowledge durable state")
	}
}

func TestCancelCredentialRotationRestoresPreviousToken(t *testing.T) {
	a := &agent{
		statePath: filepath.Join(t.TempDir(), "state.json"),
		st:        state{AgentID: "agent-1", AgentToken: "old"},
		send:      make(chan []byte, 2),
	}
	a.rotateCredential(protocol.CredentialRotateMsg{RotationID: "rotation-1", AgentToken: "new"})
	<-a.send // persisted acknowledgement
	a.cancelCredentialRotation(protocol.CredentialRotationCancelMsg{RotationID: "rotation-1"})

	if a.st.AgentToken != "old" || a.st.PendingRotationID != "" || a.st.PreviousAgentToken != "" || a.st.PendingCancellationID != "rotation-1" {
		t.Fatalf("state = %+v, want restored credential", a.st)
	}
	select {
	case raw := <-a.send:
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode cancellation: %v", err)
		}
		if env.Type != protocol.MsgCredentialRotationCanceled {
			t.Fatalf("message type = %q, want %q", env.Type, protocol.MsgCredentialRotationCanceled)
		}
	default:
		t.Fatal("cancellation did not acknowledge restoration")
	}

	// The confirmation survives a dropped frame and is retried from the state
	// file after a reconnect or restart.
	restarted := &agent{statePath: a.statePath, send: make(chan []byte, 1)}
	restarted.loadState()
	restarted.flushCredentialRotationCancellation()
	select {
	case raw := <-restarted.send:
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode retried cancellation: %v", err)
		}
		if env.Type != protocol.MsgCredentialRotationCanceled {
			t.Fatalf("retried message type = %q, want %q", env.Type, protocol.MsgCredentialRotationCanceled)
		}
	default:
		t.Fatal("cancellation confirmation was not retried after restart")
	}

	restarted.ackCredentialRotationCancellation("rotation-1")
	if restarted.st.PendingCancellationID != "" {
		t.Fatalf("cancellation confirmation remained pending after server acknowledgement: %+v", restarted.st)
	}
}

func TestCanceledRotationRejectsLateReplacement(t *testing.T) {
	a := &agent{
		statePath: filepath.Join(t.TempDir(), "state.json"),
		st:        state{AgentID: "agent-1", AgentToken: "old", PendingCancellationID: "rotation-1"},
		send:      make(chan []byte, 1),
	}
	a.rotateCredential(protocol.CredentialRotateMsg{RotationID: "rotation-1", AgentToken: "new"})

	if a.st.AgentToken != "old" || a.st.PendingRotationID != "" {
		t.Fatalf("late rotation changed canceled state: %+v", a.st)
	}
	select {
	case raw := <-a.send:
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode cancellation retry: %v", err)
		}
		if env.Type != protocol.MsgCredentialRotationCanceled {
			t.Fatalf("message type = %q, want %q", env.Type, protocol.MsgCredentialRotationCanceled)
		}
	default:
		t.Fatal("late rotation did not retry cancellation confirmation")
	}
}

func TestNewRotationConfirmsPriorCancellation(t *testing.T) {
	a := &agent{
		statePath: filepath.Join(t.TempDir(), "state.json"),
		st:        state{AgentID: "agent-1", AgentToken: "old", PendingCancellationID: "rotation-1"},
		send:      make(chan []byte, 1),
	}
	a.rotateCredential(protocol.CredentialRotateMsg{RotationID: "rotation-2", AgentToken: "new"})

	if a.st.AgentToken != "new" || a.st.PendingRotationID != "rotation-2" || a.st.PendingCancellationID != "" {
		t.Fatalf("new rotation did not supersede confirmed cancellation: %+v", a.st)
	}
	select {
	case raw := <-a.send:
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode rotation acknowledgement: %v", err)
		}
		if env.Type != protocol.MsgCredentialRotated {
			t.Fatalf("message type = %q, want %q", env.Type, protocol.MsgCredentialRotated)
		}
	default:
		t.Fatal("new rotation did not acknowledge durable state")
	}
}
