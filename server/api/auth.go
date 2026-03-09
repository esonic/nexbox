package api

import (
	"encoding/json"
	"log"
	"net/http"

	"gitlab.okg.com/sre_ai/boxnex/config"
)

// HandleAuthLogout clears the stored token.
// POST /api/auth/logout
func (s *Server) HandleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.Config.Update(func(c *config.Config) {
		c.Token = ""
	}); err != nil {
		log.Printf("[auth] failed to remove token: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to remove token")
		return
	}

	data, _ := json.Marshal(map[string]interface{}{"type": "auth:logout"})
	s.Hub.BroadcastRaw(data)

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

