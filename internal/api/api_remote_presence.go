package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/remote"
)

// listRemotePresence reports the live remote attachments, either for the whole
// fleet or for one device (?agent_id=...). The console polls it so a technician
// opening a machine can see that somebody else is already there.
//
// Sessions are never blocked on this basis — two operators on one endpoint is a
// legitimate hand-off. The failure this prevents is each of them thinking they
// are alone.
func (s *Server) listRemotePresence(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		agentID = chi.URLParam(r, "id")
	}
	viewers := s.presence.All()
	if agentID != "" {
		viewers = s.presence.ForAgent(agentID)
	}
	// A scoped user must not learn about devices outside their scope, so the
	// list is filtered the same way every other device-bearing response is.
	if scope := s.scopeUserID(r); scope != "" {
		allowed := viewers[:0]
		for _, v := range viewers {
			ok, err := s.store.AgentAllowedForUser(r.Context(), scope, v.AgentID)
			if err == nil && ok {
				allowed = append(allowed, v)
			}
		}
		viewers = allowed
	}
	// Resolved here rather than in the console: reading who is connected needs
	// only devices.view, and making the name depend on a second permission left
	// most operators staring at a raw user id.
	type namedViewer struct {
		remote.Viewer
		UserName string `json:"user_name"`
		// A guest someone let in: their name as they gave it, and the
		// technician supervising them, so nobody mistakes an outsider on the
		// screen for a colleague.
		Guest       bool   `json:"guest,omitempty"`
		SponsorName string `json:"sponsor_name,omitempty"`
	}
	named := make([]namedViewer, 0, len(viewers))
	names := map[string]string{}
	userName := func(id string) string {
		label, ok := names[id]
		if !ok {
			label = id
			if user, err := s.store.GetUserByID(r.Context(), id); err == nil && user != nil && user.Username != "" {
				label = user.Username
			} else if strings.HasPrefix(id, "ai:") {
				label = "AI assistant"
			}
			names[id] = label
		}
		return label
	}
	for _, v := range viewers {
		if shareID, ok := strings.CutPrefix(v.User, "guest:"); ok {
			nv := namedViewer{Viewer: v, UserName: "guest", Guest: true}
			if sh, err := s.store.GetRemoteShare(r.Context(), shareID); err == nil {
				nv.UserName = sh.GuestName
				nv.SponsorName = userName(sh.CreatedBy)
			}
			named = append(named, nv)
			continue
		}
		named = append(named, namedViewer{Viewer: v, UserName: userName(v.User)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewers": named})
}
