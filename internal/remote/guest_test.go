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

// What the endpoint may send to a guest: the screen and what describes it,
// never the endpoint's clipboard, chat, or file contents.
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
		{"chat from the user", text, `{"t":"chat","from":"user","text":"hi"}`, false},
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
