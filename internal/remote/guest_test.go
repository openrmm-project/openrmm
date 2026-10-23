package remote

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// What a guest may send to the endpoint, in each mode. Everything not listed
// as allowed must be dropped — the list is the guarantee for sidecars that do
// not know about guests.
func TestGuestPolicyFromViewer(t *testing.T) {
	text := websocket.TextMessage
	cases := []struct {
		name          string
		mt            int
		frame         string
		view, control bool
	}{
		{"keyframe request", text, `{"t":"kf"}`, true, true},
		{"monitor choice", text, `{"t":"mon","id":1}`, true, true},
		{"pointer move", text, `{"t":"m","x":0.5,"y":0.5}`, false, true},
		{"button", text, `{"t":"b","x":0.5,"y":0.5,"button":0,"down":true}`, false, true},
		{"wheel", text, `{"t":"w","dy":120}`, false, true},
		{"key", text, `{"t":"k","code":65,"down":true}`, false, true},
		{"clipboard", text, `{"t":"clip","text":"x","paste":true}`, false, false},
		{"chat", text, `{"t":"chat","text":"hi"}`, false, false},
		{"ctrl alt delete", text, `{"t":"cad"}`, false, false},
		{"input lock", text, `{"t":"lockinput","enable":true}`, false, false},
		{"uac", text, `{"t":"uac","enable":true}`, false, false},
		{"file list", text, `{"t":"fls","id":1,"path":"/"}`, false, false},
		{"file download", text, `{"t":"fget","id":1,"path":"/etc/shadow"}`, false, false},
		{"file upload", text, `{"t":"fput","id":1,"path":"/tmp/x","size":1}`, false, false},
		{"file delete", text, `{"t":"fdel","id":1,"path":"/"}`, false, false},
		{"unknown type", text, `{"t":"run","cmd":"x"}`, false, false},
		{"no type", text, `{"x":1}`, false, false},
		{"not json", text, `kf`, false, false},
		{"file chunk", websocket.BinaryMessage, "\x02\x01data", false, false},
		{"video, backwards", websocket.BinaryMessage, "\x01data", false, false},
		// The sidecar reads "t" into a struct: the last duplicate wins, and the
		// match ignores case. The filter must reach the same verdict on the same
		// bytes, or an allowed-looking frame would carry a refused command.
		{"duplicate key, last wins", text, `{"t":"kf","t":"cad"}`, false, false},
		{"cased duplicate, last wins", text, `{"t":"kf","T":"fget","path":"/"}`, false, false},
		{"duplicate resolving to allowed", text, `{"t":"cad","t":"kf"}`, true, true},
		{"oversized", text, `{"t":"k","pad":"` + strings.Repeat("a", guestFrameLimit) + `"}`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (GuestPolicy{}).FromViewer(c.mt, []byte(c.frame)); got != c.view {
				t.Errorf("view only: allowed=%v, want %v", got, c.view)
			}
			if got := (GuestPolicy{Control: true}).FromViewer(c.mt, []byte(c.frame)); got != c.control {
				t.Errorf("control: allowed=%v, want %v", got, c.control)
			}
		})
	}
}

// What the endpoint may send to a guest: the screen and what describes it, and
// what the person at the machine writes in the chat — never the endpoint's
// clipboard or file contents.
func TestGuestPolicyFromAgent(t *testing.T) {
	text := websocket.TextMessage
	cases := []struct {
		name  string
		mt    int
		frame string
		allow bool
	}{
		{"video tile", websocket.BinaryMessage, "\x01\x00frame", true},
		{"file chunk", websocket.BinaryMessage, "\x02\x01secret", false},
		{"empty binary", websocket.BinaryMessage, "", false},
		{"hello", text, `{"t":"hello","v":"0.17.1"}`, true},
		{"monitors", text, `{"t":"monitors","list":[]}`, true},
		{"notice", text, `{"t":"notice","text":"Wayland"}`, true},
		{"clipboard mirror", text, `{"t":"clip","text":"password"}`, false},
		{"chat from the user", text, `{"t":"chat","from":"user","text":"hi"}`, true},
		{"file listing", text, `{"t":"flist","entries":[]}`, false},
		{"file metadata", text, `{"t":"fmeta","id":1,"name":"x"}`, false},
		{"input lock state", text, `{"t":"inputlocked","enabled":true}`, false},
		{"not json", text, `hello`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (GuestPolicy{Control: true}).FromAgent(c.mt, []byte(c.frame)); got != c.allow {
				t.Errorf("allowed=%v, want %v", got, c.allow)
			}
		})
	}
}

// The relay applies the policy to a real pair: refused frames vanish, allowed
// ones arrive in order, and the session survives a refused frame.
func TestGuestRelayFiltersBothDirections(t *testing.T) {
	r := NewRelay()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		if req.URL.Query().Get("role") == "viewer" {
			_ = r.JoinGuest("session-1", "agent-1", conn, GuestPolicy{})
			return
		}
		_ = r.Join("session-1", "agent-1", "agent", conn)
	}))
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	agent, _, err := websocket.DefaultDialer.Dial(base+"?role=agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	guest, _, err := websocket.DefaultDialer.Dial(base+"?role=viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()

	send := func(c *websocket.Conn, mt int, data string) {
		t.Helper()
		if err := c.WriteMessage(mt, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(c *websocket.Conn) string {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(data)
	}

	// Guest to endpoint, view only: the click and the file request are dropped,
	// the keyframe request arrives.
	send(guest, websocket.TextMessage, `{"t":"b","x":0.1,"y":0.1,"button":0,"down":true}`)
	send(guest, websocket.TextMessage, `{"t":"fget","id":1,"path":"/etc/shadow"}`)
	send(guest, websocket.BinaryMessage, "\x02\x01upload")
	send(guest, websocket.TextMessage, `{"t":"kf"}`)
	if got := read(agent); got != `{"t":"kf"}` {
		t.Fatalf("endpoint received %q, want only the keyframe request", got)
	}

	// Endpoint to guest: the clipboard and the file chunk are dropped, the
	// screen arrives.
	send(agent, websocket.TextMessage, `{"t":"clip","text":"secret"}`)
	send(agent, websocket.BinaryMessage, "\x02\x01file")
	send(agent, websocket.BinaryMessage, "\x01tile")
	if got := read(guest); got != "\x01tile" {
		t.Fatalf("guest received %q, want only the video frame", got)
	}
}

// A guest's chat line is heard by the hook but never reaches the endpoint of
// the guest's own session; what the person at the machine writes there reaches
// the guest and is heard too; and the server can add a line to either peer.
func TestRelayChatHookAndSend(t *testing.T) {
	type heard struct{ agentID, sessionID, role, text string }
	lines := make(chan heard, 8)
	r := NewRelay()
	r.SetChatHook(func(agentID, sessionID, role, text string) {
		lines <- heard{agentID, sessionID, role, text}
	})
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		if req.URL.Query().Get("role") == "viewer" {
			_ = r.JoinGuest("session-1", "agent-1", conn, GuestPolicy{})
			return
		}
		_ = r.Join("session-1", "agent-1", "agent", conn)
	}))
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	agent, _, err := websocket.DefaultDialer.Dial(base+"?role=agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	guest, _, err := websocket.DefaultDialer.Dial(base+"?role=viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	send := func(c *websocket.Conn, data string) {
		t.Helper()
		if err := c.WriteMessage(websocket.TextMessage, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(c *websocket.Conn) string {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(data)
	}
	hear := func() heard {
		t.Helper()
		select {
		case h := <-lines:
			return h
		case <-time.After(2 * time.Second):
			t.Fatal("the hook heard nothing")
			return heard{}
		}
	}

	send(guest, `{"t":"chat","text":"hello from the guest"}`)
	send(guest, `{"t":"kf"}`)
	if got := read(agent); got != `{"t":"kf"}` {
		t.Fatalf("endpoint received %q; the guest's chat must not reach it", got)
	}
	if h := hear(); h != (heard{"agent-1", "session-1", "viewer", "hello from the guest"}) {
		t.Fatalf("hook heard %+v", h)
	}

	send(agent, `{"t":"chat","from":"user","text":"hi"}`)
	if got := read(guest); got != `{"t":"chat","from":"user","text":"hi"}` {
		t.Fatalf("guest received %q", got)
	}
	if h := hear(); h != (heard{"agent-1", "session-1", "agent", "hi"}) {
		t.Fatalf("hook heard %+v", h)
	}

	if err := r.Send("session-1", "viewer", []byte(`{"t":"chat","from":"tech","text":"to the guest"}`)); err != nil {
		t.Fatal(err)
	}
	if got := read(guest); !strings.Contains(got, "to the guest") {
		t.Fatalf("guest received %q", got)
	}
	if err := r.Send("session-1", "agent", []byte(`{"t":"chat","from":"tech","text":"to the endpoint"}`)); err != nil {
		t.Fatal(err)
	}
	if got := read(agent); !strings.Contains(got, "to the endpoint") {
		t.Fatalf("endpoint received %q", got)
	}
	if err := r.Send("no-such-session", "viewer", []byte(`{}`)); err == nil {
		t.Fatal("a line was sent to a session that does not exist")
	}
}

// chatText reads "t" as the sidecar does: with duplicate keys the last wins,
// in any case.
func TestChatText(t *testing.T) {
	cases := []struct {
		frame string
		text  string
		ok    bool
	}{
		{`{"t":"chat","text":"hi"}`, "hi", true},
		{`{"t":"chat","text":""}`, "", false},
		{`{"t":"kf","T":"chat","text":"hi"}`, "hi", true},
		{`{"t":"chat","T":"kf","text":"hi"}`, "", false},
		{`{"t":"clip","text":"secret"}`, "", false},
		{`{"t":"chat","text":"` + strings.Repeat("a", chatFrameLimit) + `"}`, "", false},
		{`not json`, "", false},
	}
	for _, c := range cases {
		if text, ok := chatText([]byte(c.frame)); text != c.text || ok != c.ok {
			t.Errorf("chatText(%.40q) = %q, %v; want %q, %v", c.frame, text, ok, c.text, c.ok)
		}
	}
}

// The relay remembers whether an endpoint's sidecar shows a guest's chat line
// under the guest's name, from the hello it sends, before passing it on.
func TestRelayRecordsGuestChatSupport(t *testing.T) {
	r := NewRelay()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		_ = r.Join("session-1", "agent-1", req.URL.Query().Get("role"), conn)
	}))
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	agent, _, err := websocket.DefaultDialer.Dial(base+"?role=agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	viewer, _, err := websocket.DefaultDialer.Dial(base+"?role=viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	hello := func(frame string) {
		t.Helper()
		if err := agent.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatal(err)
		}
		_ = viewer.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := viewer.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	if r.GuestChat("session-1") {
		t.Fatal("support assumed before any hello")
	}
	hello(`{"t":"hello","v":"0.17.1"}`)
	if r.GuestChat("session-1") {
		t.Fatal("a 0.17 hello read as support")
	}
	hello(`{"t":"hello","v":"0.18.0","guest_chat":true}`)
	if !r.GuestChat("session-1") {
		t.Fatal("a 0.18 hello was not recorded")
	}
	// A viewer cannot claim it for the endpoint.
	if err := viewer.WriteMessage(websocket.TextMessage, []byte(`{"t":"hello","guest_chat":false}`)); err != nil {
		t.Fatal(err)
	}
	_ = agent.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := agent.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if !r.GuestChat("session-1") {
		t.Fatal("the viewer's frame changed what the endpoint announced")
	}
	if r.GuestChat("no-such-session") {
		t.Fatal("support reported for a session that does not exist")
	}
}
