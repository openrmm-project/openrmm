package remote

import (
	"sync"
	"testing"
	"time"
)

func TestPresenceAnnouncesASecondViewerWithoutBlockingIt(t *testing.T) {
	p := NewPresence()
	first := p.Join(Viewer{SessionID: "s1", AgentID: "agent-a", User: "ana", Kind: "native", Since: time.Unix(100, 0)})
	// The second attachment must succeed: two technicians on one endpoint is a
	// hand-off, not a conflict. What must not happen is either of them being
	// unaware of the other.
	p.Join(Viewer{SessionID: "s2", AgentID: "agent-a", User: "bruno", Kind: "files", Since: time.Unix(200, 0)})

	viewers := p.ForAgent("agent-a")
	if len(viewers) != 2 {
		t.Fatalf("expected both viewers, got %d", len(viewers))
	}
	if viewers[0].User != "ana" || viewers[1].User != "bruno" {
		t.Fatalf("oldest attachment should come first, got %q then %q", viewers[0].User, viewers[1].User)
	}
	if viewers[1].Kind != "files" {
		t.Errorf("the kind must survive so the console can say what the session is: %q", viewers[1].Kind)
	}

	first()
	if viewers := p.ForAgent("agent-a"); len(viewers) != 1 || viewers[0].User != "bruno" {
		t.Fatalf("leaving must remove only that viewer, got %+v", viewers)
	}
}

func TestPresenceForgetsAnEndpointOnceEveryoneLeaves(t *testing.T) {
	p := NewPresence()
	leave := p.Join(Viewer{SessionID: "s1", AgentID: "agent-a", User: "ana"})
	leave()
	leave() // a dropped socket can fire the cleanup twice; it must stay harmless
	if viewers := p.ForAgent("agent-a"); len(viewers) != 0 {
		t.Fatalf("a ghost attachment survived: %+v", viewers)
	}
	if all := p.All(); len(all) != 0 {
		t.Fatalf("the fleet view still lists it: %+v", all)
	}
}

func TestPresenceStampsJoinTimeWhenTheCallerDoesNot(t *testing.T) {
	p := NewPresence()
	p.Join(Viewer{SessionID: "s1", AgentID: "agent-a", User: "ana"})
	viewers := p.ForAgent("agent-a")
	if len(viewers) != 1 || viewers[0].Since.IsZero() {
		t.Fatal("a viewer with no timestamp cannot be shown as \"connected since\"")
	}
}

func TestPresenceIsSafeUnderConcurrentSessions(t *testing.T) {
	p := NewPresence()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			leave := p.Join(Viewer{SessionID: string(rune('a' + n%26)), AgentID: "agent-a", User: "u"})
			p.All()
			p.ForAgent("agent-a")
			leave()
		}(i)
	}
	wg.Wait()
	if viewers := p.ForAgent("agent-a"); len(viewers) != 0 {
		t.Fatalf("attachments outlived their sessions: %+v", viewers)
	}
}

func TestPresenceRefusesTheSameOperatorTwiceOnOneScreen(t *testing.T) {
	p := NewPresence()
	leave, existing := p.JoinExclusive(Viewer{SessionID: "s1", AgentID: "agent-a", User: "ana", Kind: "native"})
	if existing != nil {
		t.Fatalf("the first session was refused: %+v", existing)
	}
	_, existing = p.JoinExclusive(Viewer{SessionID: "s2", AgentID: "agent-a", User: "ana", Kind: "native"})
	if existing == nil || existing.SessionID != "s1" {
		t.Fatalf("a second desktop for the same operator was admitted (existing=%+v)", existing)
	}
	if viewers := p.ForAgent("agent-a"); len(viewers) != 1 {
		t.Fatalf("the refused session was still recorded: %+v", viewers)
	}
	leave()
	if _, existing = p.JoinExclusive(Viewer{SessionID: "s3", AgentID: "agent-a", User: "ana", Kind: "native"}); existing != nil {
		t.Fatal("leaving did not free the operator's slot")
	}
}

func TestPresenceExclusiveStillAllowsWhatIsNotADuplicate(t *testing.T) {
	p := NewPresence()
	p.JoinExclusive(Viewer{SessionID: "s1", AgentID: "agent-a", User: "ana", Kind: "native"})
	for _, v := range []Viewer{
		{SessionID: "s2", AgentID: "agent-a", User: "bruno", Kind: "native"}, // a colleague: a hand-off
		{SessionID: "s3", AgentID: "agent-b", User: "ana", Kind: "native"},   // another machine
		{SessionID: "s4", AgentID: "agent-a", User: "ana", Kind: "files"},    // not a second screen
	} {
		if _, existing := p.JoinExclusive(v); existing != nil {
			t.Errorf("%s/%s/%s was refused as a duplicate of %s", v.User, v.AgentID, v.Kind, existing.SessionID)
		}
	}
}

func TestPresenceRemoveFreesAReplacedSessionAtOnce(t *testing.T) {
	p := NewPresence()
	leave, _ := p.JoinExclusive(Viewer{SessionID: "old", AgentID: "agent-a", User: "ana", Kind: "native"})
	if got := p.ForUser("agent-a", "ana", "native"); len(got) != 1 || got[0].SessionID != "old" {
		t.Fatalf("ForUser = %+v", got)
	}
	p.Remove("agent-a", "old")
	if _, existing := p.JoinExclusive(Viewer{SessionID: "new", AgentID: "agent-a", User: "ana", Kind: "native"}); existing != nil {
		t.Fatal("the replacing session was refused by the one it replaced")
	}
	leave() // the old socket's late cleanup must not remove the new session
	if got := p.ForUser("agent-a", "ana", "native"); len(got) != 1 || got[0].SessionID != "new" {
		t.Fatalf("after the old cleanup ForUser = %+v", got)
	}
}

func TestPresenceExclusiveAdmitsOneOfManySimultaneousTabs(t *testing.T) {
	p := NewPresence()
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, existing := p.JoinExclusive(Viewer{SessionID: string(rune('A' + i)), AgentID: "agent-a", User: "ana", Kind: "native"}); existing == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("%d tabs got in at once, want 1", admitted)
	}
}
