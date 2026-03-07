package api

import (
	"log"
	"net/http"
)

// GetUpdateStatus is a lightweight liveness probe used by the frontend to
// detect when the server is back online after a restart.
// GET /api/update/status → {"ok": true}
func (s *Server) GetUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// TriggerRestart performs a syscall.Exec to restart into the updated binary.
// POST /api/update/restart
func (s *Server) TriggerRestart(w http.ResponseWriter, r *http.Request) {
	if s.Updater == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	go func() {
		if err := s.Updater.Restart(); err != nil {
			log.Printf("[updater] restart failed: %v", err)
		}
	}()
}
