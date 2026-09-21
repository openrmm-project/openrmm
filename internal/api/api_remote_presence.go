package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
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
	writeJSON(w, http.StatusOK, map[string]any{"viewers": viewers})
}
