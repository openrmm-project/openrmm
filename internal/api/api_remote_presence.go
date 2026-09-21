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
	}
	named := make([]namedViewer, 0, len(viewers))
	names := map[string]string{}
	for _, v := range viewers {
		label, ok := names[v.User]
		if !ok {
			label = v.User
			if user, err := s.store.GetUserByID(r.Context(), v.User); err == nil && user != nil && user.Username != "" {
				label = user.Username
			} else if strings.HasPrefix(v.User, "ai:") {
				label = "AI assistant"
			}
			names[v.User] = label
		}
		named = append(named, namedViewer{Viewer: v, UserName: label})
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewers": named})
}
