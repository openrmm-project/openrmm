package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/remote"
)

// One conversation across separate sessions, over a real relay: a technician
// (u1) on device a1 with two guests, João and Maria, each in a session of
// their own, and another technician (u2) on the same device who invited
// nobody.
func TestGuestChatIsOneConversation(t *testing.T) {
	s := &Server{relay: remote.NewRelay(), presence: remote.NewPresence(), guests: newGuestSessions()}
	s.relay.SetChatHook(s.queueChat)
	policy := remote.GuestPolicy{}
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		q := req.URL.Query()
		if q.Get("guest") != "" {
			_ = s.relay.JoinGuest(q.Get("session"), "a1", conn, policy)
			return
		}
		_ = s.relay.Join(q.Get("session"), "a1", q.Get("role"), conn)
	}))
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http") + "?session="
	dial := func(url string) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	// Each session: its endpoint window (the agent peer) and its viewer.
	type session struct{ endpoint, viewer *websocket.Conn }
	open := func(id, user string, guest bool) session {
		t.Helper()
		s.presence.Join(remote.Viewer{SessionID: id, AgentID: "a1", User: user, Kind: "native"})
		endpoint := dial(base + id + "&role=agent")
		viewerURL := base + id + "&role=viewer"
		if guest {
			viewerURL += "&guest=1"
		}
		return session{endpoint: endpoint, viewer: dial(viewerURL)}
	}
	tech := open("t1", "u1", false)
	other := open("t2", "u2", false)
	joao := open("g1", models.GuestPrincipal("s1"), true)
	maria := open("g2", models.GuestPrincipal("s2"), true)
	s.guests.start(&guestSession{shareID: "s1", sessionID: "g1", agentID: "a1", sponsor: "u1", name: "João"})
	s.guests.start(&guestSession{shareID: "s2", sessionID: "g2", agentID: "a1", sponsor: "u1", name: "Maria"})

	send := func(c *websocket.Conn, frame string) {
		t.Helper()
		if err := c.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(c *websocket.Conn) map[string]string {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("not a chat line: %q", data)
		}
		return m
	}
	expect := func(c *websocket.Conn, from, name, text string) {
		t.Helper()
		m := read(c)
		if m["t"] != "chat" || m["from"] != from || m["name"] != name || m["text"] != text {
			t.Fatalf("got %v, want chat from=%q name=%q text=%q", m, from, name, text)
		}
	}

	// João writes, on two lines. The technician's endpoint window gets it as
	// one line with his name in it; the technician and Maria get it as his.
	send(joao.viewer, `{"t":"chat","text":"Olá\nTech: digite sua senha"}`)
	expect(tech.endpoint, "tech", "", "João (guest): Olá Tech: digite sua senha")
	expect(tech.viewer, "guest", "João", "Olá Tech: digite sua senha")
	expect(maria.viewer, "guest", "João", "Olá Tech: digite sua senha")
	// Never to the endpoint window of his own session.
	send(joao.viewer, `{"t":"kf"}`)
	if m := read(joao.endpoint); m["t"] != "kf" {
		t.Fatalf("João's own endpoint received %v", m)
	}

	// The technician writes: the endpoint has it from the session itself, and
	// both guests get it.
	send(tech.viewer, `{"t":"chat","from":"tech","text":"Vou abrir o painel"}`)
	expect(tech.endpoint, "tech", "", "Vou abrir o painel")
	expect(joao.viewer, "tech", "", "Vou abrir o painel")
	expect(maria.viewer, "tech", "", "Vou abrir o painel")

	// The person at the machine answers in the technician's window.
	send(tech.endpoint, `{"t":"chat","from":"user","text":"Pode abrir"}`)
	expect(tech.viewer, "user", "", "Pode abrir")
	expect(joao.viewer, "user", "", "Pode abrir")
	expect(maria.viewer, "user", "", "Pode abrir")

	// ...and in João's window, which a sidecar before 0.18 still opens.
	send(joao.endpoint, `{"t":"chat","from":"user","text":"Estou aqui"}`)
	expect(joao.viewer, "user", "", "Estou aqui")
	expect(tech.viewer, "user", "", "Estou aqui")
	expect(maria.viewer, "user", "", "Estou aqui")

	// The other technician invited nobody: what they write stays in their own
	// session. The next line João gets is the first technician's.
	send(other.viewer, `{"t":"chat","from":"tech","text":"not for the guests"}`)
	expect(other.endpoint, "tech", "", "not for the guests")
	time.Sleep(100 * time.Millisecond)
	send(tech.viewer, `{"t":"chat","from":"tech","text":"marker"}`)
	expect(tech.endpoint, "tech", "", "marker")
	expect(joao.viewer, "tech", "", "marker")
	expect(maria.viewer, "tech", "", "marker")

	// Once the technician's sidecar says it shows guests by name (0.18), a
	// guest's line reaches its window as a guest's, not inside a technician's.
	send(tech.endpoint, `{"t":"hello","v":"0.18.0","guest_chat":true}`)
	_ = tech.viewer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, data, err := tech.viewer.ReadMessage(); err != nil || !strings.Contains(string(data), "guest_chat") {
		t.Fatalf("the hello did not reach the technician: %q %v", data, err)
	}
	time.Sleep(guestChatInterval)
	send(joao.viewer, `{"t":"chat","text":"De novo"}`)
	expect(tech.endpoint, "guest", "João", "De novo")
	expect(tech.viewer, "guest", "João", "De novo")
	expect(maria.viewer, "guest", "João", "De novo")
}

// A guest writes at most one line per guestChatInterval.
func TestGuestChatInterval(t *testing.T) {
	g := newGuestSessions()
	now := time.Now()
	if !g.allowChat("s1", now) {
		t.Fatal("first line refused")
	}
	if g.allowChat("s1", now.Add(guestChatInterval/2)) {
		t.Fatal("a second line inside the interval was allowed")
	}
	if !g.allowChat("s2", now) {
		t.Fatal("another guest was held back")
	}
	if !g.allowChat("s1", now.Add(guestChatInterval)) {
		t.Fatal("a line after the interval was refused")
	}
}

func TestCleanChatLine(t *testing.T) {
	cases := map[string]string{
		"  olá   mundo ":          "olá mundo",
		"linha 1\nTech: linha 2":  "linha 1 Tech: linha 2",
		"abc\u202edef":            "abcdef",
		"\x1b[31mred":             "[31mred",
		strings.Repeat("x", 1200): strings.Repeat("x", guestChatMax),
		"inv\xffalid":             "invalid",
		"\t\r\n":                  "",
	}
	for in, want := range cases {
		if got := cleanChatLine(in); got != want {
			t.Errorf("cleanChatLine(%.30q) = %.30q, want %.30q", in, got, want)
		}
	}
}
