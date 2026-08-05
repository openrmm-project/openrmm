package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// AgentVersions backs the capability gates, so its SQL decides whether a device
// is offered work it cannot do. The job service tests use a fake; this is the
// only place the query itself runs.
func TestAgentVersionsIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	current, err := st.CreateAgent(ctx, "current-"+suffix, "linux", "amd64", "0.4.9", "tok-current-"+suffix, nil)
	if err != nil {
		t.Fatal(err)
	}
	outdated, err := st.CreateAgent(ctx, "outdated-"+suffix, "windows", "amd64", "0.4.8", "tok-outdated-"+suffix, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.AgentVersions(ctx, []string{current.ID, outdated.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[current.ID].Version != "0.4.9" || got[current.ID].Hostname != "current-"+suffix {
		t.Errorf("current agent = %+v", got[current.ID])
	}
	if got[outdated.ID].Version != "0.4.8" {
		t.Errorf("outdated agent version = %q, want 0.4.8", got[outdated.ID].Version)
	}

	// A missing id must be absent rather than zero-valued: a caller has to be able
	// to tell "this agent reports no version" from "there is no such agent". Both
	// bar a capability gate, but only one of them is a bug worth chasing.
	ghost := uuid.NewString()
	got, err = st.AgentVersions(ctx, []string{current.ID, ghost})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := got[ghost]; present {
		t.Error("an unknown agent id should not appear in the result")
	}
	if len(got) != 1 {
		t.Errorf("got %d rows, want only the known agent", len(got))
	}

	// An empty id list is a legitimate call (a deployment with no targets) and
	// must not error or return the whole fleet.
	if got, err = st.AgentVersions(ctx, nil); err != nil {
		t.Errorf("an empty id list should not error: %v", err)
	} else if len(got) != 0 {
		t.Errorf("an empty id list returned %d rows", len(got))
	}

	// An agent that has never reported a version comes back as the empty string,
	// not a NULL scan error — the gate then reads it as "too old", which is the
	// safe direction for a value the endpoint reports about itself.
	blank, err := st.CreateAgent(ctx, "blank-"+suffix, "linux", "amd64", "", "tok-blank-"+suffix, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err = st.AgentVersions(ctx, []string{blank.ID})
	if err != nil {
		t.Fatalf("an agent with no reported version should not break the query: %v", err)
	}
	if info, present := got[blank.ID]; !present || info.Version != "" {
		t.Errorf("blank-version agent = %+v, present=%t", info, present)
	}
}
