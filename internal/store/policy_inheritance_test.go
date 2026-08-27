package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// policyFixture is a two-tenant org with one agent on each side, which is what
// the inheritance and visibility queries actually have to tell apart.
//
//	clientA ── siteA ── agentA
//	clientB ── siteB ── agentB
type policyFixture struct {
	t                *testing.T
	store            *Store
	ctx              context.Context
	clientA, clientB *models.Client
	siteA, siteB     *models.Site
	agentA, agentB   *models.Agent
}

func newPolicyFixture(t *testing.T) *policyFixture {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	f := &policyFixture{t: t, store: st, ctx: ctx}
	suffix := uuid.NewString()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f.clientA, err = st.CreateClient(ctx, "A-"+suffix)
	must(err)
	f.clientB, err = st.CreateClient(ctx, "B-"+suffix)
	must(err)
	f.siteA, err = st.CreateSite(ctx, f.clientA.ID, "site-A-"+suffix)
	must(err)
	f.siteB, err = st.CreateSite(ctx, f.clientB.ID, "site-B-"+suffix)
	must(err)
	f.agentA, err = st.CreateAgent(ctx, "host-A-"+suffix, "linux", "amd64", "1.0.0", "tok-A-"+suffix, &f.siteA.ID, nil)
	must(err)
	f.agentB, err = st.CreateAgent(ctx, "host-B-"+suffix, "linux", "amd64", "1.0.0", "tok-B-"+suffix, &f.siteB.ID, nil)
	must(err)
	return f
}

// policyWithContents creates a policy holding one check and one task.
func (f *policyFixture) policyWithContents(name string) *models.Policy {
	f.t.Helper()
	p, err := f.store.CreatePolicy(f.ctx, name, "", "")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.store.CreateCheck(f.ctx, &models.Check{
		PolicyID: &p.ID, Type: "diskspace", Name: name + " disk", IntervalSec: 300, Enabled: true,
	}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.store.CreateTask(f.ctx, &models.Task{
		PolicyID: &p.ID, Name: name + " nightly", Shell: "bash", ScheduleKind: "interval",
		IntervalSec: 3600, Enabled: true,
	}); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *policyFixture) inherited(a *models.Agent, site *models.Site, client *models.Client) (checks, tasks []string) {
	f.t.Helper()
	cs, err := f.store.ChecksForAgent(f.ctx, a.ID, &site.ID, &client.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	ts, err := f.store.TasksForAgent(f.ctx, a.ID, &site.ID, &client.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, c := range cs {
		checks = append(checks, c.Name)
	}
	for _, t := range ts {
		tasks = append(tasks, t.Name)
	}
	return checks, tasks
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// The whole point of a policy: an agent picks up the checks and tasks of every
// policy assigned to it, to its site, or to its client. This is the SQL in
// policyFilter, and nothing exercised it — a break here is silent, because
// nothing errors, agents simply stop being sent what they should run.
func TestPolicyContentsReachAgentsInScopeIntegration(t *testing.T) {
	f := newPolicyFixture(t)

	bySite := f.policyWithContents("by-site")
	byClient := f.policyWithContents("by-client")
	byAgent := f.policyWithContents("by-agent")
	// Created but never assigned: the control that proves the filter selects
	// rather than returning everything.
	f.policyWithContents("unassigned")

	for _, a := range []struct {
		policy             *models.Policy
		scopeType, scopeID string
	}{
		{bySite, models.ScopeSite, f.siteA.ID},
		{byClient, models.ScopeClient, f.clientA.ID},
		{byAgent, models.ScopeAgent, f.agentA.ID},
	} {
		if _, err := f.store.AssignPolicy(f.ctx, a.policy.ID, a.scopeType, a.scopeID); err != nil {
			t.Fatal(err)
		}
	}

	checks, tasks := f.inherited(f.agentA, f.siteA, f.clientA)
	for _, want := range []string{"by-site", "by-client", "by-agent"} {
		if !contains(checks, want+" disk") {
			t.Errorf("agent A did not inherit the %s check (got %v)", want, checks)
		}
		if !contains(tasks, want+" nightly") {
			t.Errorf("agent A did not inherit the %s task (got %v)", want, tasks)
		}
	}
	if contains(checks, "unassigned disk") || contains(tasks, "unassigned nightly") {
		t.Errorf("an unassigned policy reached agent A: checks=%v tasks=%v", checks, tasks)
	}

	// The neighbouring tenant must see none of it.
	otherChecks, otherTasks := f.inherited(f.agentB, f.siteB, f.clientB)
	if len(otherChecks) != 0 || len(otherTasks) != 0 {
		t.Errorf("agent B inherited another tenant's policy: checks=%v tasks=%v", otherChecks, otherTasks)
	}
}

// Moving an assignment moves what runs where. The scheduler reads this through
// AgentIDsForPolicy, which is how "Run now" decides its targets.
func TestPolicyAssignmentChangesWhoInheritsIntegration(t *testing.T) {
	f := newPolicyFixture(t)
	policy := f.policyWithContents("movable")

	assignment, err := f.store.AssignPolicy(f.ctx, policy.ID, models.ScopeSite, f.siteA.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := f.store.AgentIDsForPolicy(f.ctx, policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != f.agentA.ID {
		t.Fatalf("targets = %v, want only agent A (%s)", ids, f.agentA.ID)
	}

	// Round-trip the assignment through the getter the delete path uses.
	got, err := f.store.GetPolicyAssignment(f.ctx, assignment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PolicyID != policy.ID || got.ScopeID != f.siteA.ID {
		t.Errorf("assignment = %+v, want policy %s on site %s", got, policy.ID, f.siteA.ID)
	}

	if err := f.store.DeleteAssignment(f.ctx, assignment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AssignPolicy(f.ctx, policy.ID, models.ScopeSite, f.siteB.ID); err != nil {
		t.Fatal(err)
	}

	if checks, tasks := f.inherited(f.agentA, f.siteA, f.clientA); len(checks) != 0 || len(tasks) != 0 {
		t.Errorf("agent A still inherits after the policy moved away: checks=%v tasks=%v", checks, tasks)
	}
	checks, tasks := f.inherited(f.agentB, f.siteB, f.clientB)
	if !contains(checks, "movable disk") || !contains(tasks, "movable nightly") {
		t.Errorf("agent B did not pick the policy up: checks=%v tasks=%v", checks, tasks)
	}
	ids, err = f.store.AgentIDsForPolicy(f.ctx, policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != f.agentB.ID {
		t.Fatalf("targets after the move = %v, want only agent B (%s)", ids, f.agentB.ID)
	}

	// A policy nobody is assigned to has no targets — the case "Run now" warns
	// about before it runs nothing.
	orphan := f.policyWithContents("orphan")
	if ids, err := f.store.AgentIDsForPolicy(f.ctx, orphan.ID); err != nil || len(ids) != 0 {
		t.Errorf("unassigned policy targets = %v, err=%v, want none", ids, err)
	}
}

// What a restricted operator may see and manage. These queries are the largest
// in the file and had no coverage at all, on the same scope boundary where a
// silent privilege escalation was found.
func TestPolicyVisibilityForScopedUserIntegration(t *testing.T) {
	f := newPolicyFixture(t)

	mine := f.policyWithContents("mine")
	theirs := f.policyWithContents("theirs")
	if _, err := f.store.AssignPolicy(f.ctx, mine.ID, models.ScopeSite, f.siteA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AssignPolicy(f.ctx, theirs.ID, models.ScopeSite, f.siteB.ID); err != nil {
		t.Fatal(err)
	}

	scoped, err := f.store.CreateUserWithScopes(f.ctx, "scoped-"+uuid.NewString(), "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: f.clientA.ID}})
	if err != nil {
		t.Fatal(err)
	}

	visible, err := f.store.ListPoliciesForUser(f.ctx, scoped.ID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range visible {
		names = append(names, p.Name)
	}
	if !contains(names, "mine") {
		t.Errorf("the scoped user cannot see the policy covering their own site: %v", names)
	}
	if contains(names, "theirs") {
		t.Errorf("the scoped user can see another tenant's policy: %v", names)
	}

	if ok, err := f.store.PolicyAllowedForUser(f.ctx, scoped.ID, mine.ID); err != nil || !ok {
		t.Errorf("own policy allowed = %t, err=%v, want true", ok, err)
	}
	if ok, err := f.store.PolicyAllowedForUser(f.ctx, scoped.ID, theirs.ID); err != nil || ok {
		t.Errorf("other tenant's policy allowed = %t, err=%v, want false", ok, err)
	}

	// Assignments are filtered the same way, or the drawer would list scopes the
	// operator cannot otherwise see.
	own, err := f.store.ListPolicyAssignmentsForUser(f.ctx, scoped.ID, mine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 || own[0].ScopeID != f.siteA.ID {
		t.Errorf("visible assignments = %+v, want the one on site A", own)
	}
	other, err := f.store.ListPolicyAssignmentsForUser(f.ctx, scoped.ID, theirs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Errorf("another tenant's assignments leaked: %+v", other)
	}
}

// Deleting a policy takes its checks, tasks and assignments with it — the
// console offers the button, and the agents covered must stop receiving them.
func TestDeletingAPolicyRemovesItsContentsIntegration(t *testing.T) {
	f := newPolicyFixture(t)

	doomed := f.policyWithContents("doomed")
	survivor := f.policyWithContents("survivor")
	for _, p := range []*models.Policy{doomed, survivor} {
		if _, err := f.store.AssignPolicy(f.ctx, p.ID, models.ScopeSite, f.siteA.ID); err != nil {
			t.Fatal(err)
		}
	}
	if checks, _ := f.inherited(f.agentA, f.siteA, f.clientA); len(checks) != 2 {
		t.Fatalf("setup: agent A inherits %v, want both checks", checks)
	}

	if err := f.store.DeletePolicy(f.ctx, doomed.ID); err != nil {
		t.Fatal(err)
	}

	checks, tasks := f.inherited(f.agentA, f.siteA, f.clientA)
	if contains(checks, "doomed disk") || contains(tasks, "doomed nightly") {
		t.Errorf("a deleted policy still reaches the agent: checks=%v tasks=%v", checks, tasks)
	}
	if !contains(checks, "survivor disk") || !contains(tasks, "survivor nightly") {
		t.Errorf("deleting one policy took another with it: checks=%v tasks=%v", checks, tasks)
	}
	// The rows themselves are gone, not merely detached.
	if left, err := f.store.ListChecks(f.ctx, "", doomed.ID); err != nil || len(left) != 0 {
		t.Errorf("checks left behind = %+v, err=%v", left, err)
	}
	if left, err := f.store.ListTasks(f.ctx, "", doomed.ID); err != nil || len(left) != 0 {
		t.Errorf("tasks left behind = %+v, err=%v", left, err)
	}
	if left, err := f.store.ListPolicyAssignments(f.ctx, doomed.ID); err != nil || len(left) != 0 {
		t.Errorf("assignments left behind = %+v, err=%v", left, err)
	}
}
