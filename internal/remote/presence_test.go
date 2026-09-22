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
