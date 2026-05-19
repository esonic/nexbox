package api

import (
	"net/http"

	"github.com/esonic/nexbox/store"
)

// SearchMembers proxies fuzzy member search to the remote service.
// GET /api/members?q=xxx
func (s *Server) SearchMembers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")

	if s.Cloud == nil {
		writeJSON(w, http.StatusOK, []store.Member{})
		return
	}

	members, err := s.Cloud.SearchMembers(q)
	if writeStoreError(w, err, "SearchMembers", "failed to search members") {
		return
	}
	if members == nil {
		members = []store.Member{}
	}
	writeJSON(w, http.StatusOK, members)
}
