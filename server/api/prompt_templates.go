package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/esonic/nexbox/config"
)

const (
	maxPromptTemplates       = 200
	maxPromptTemplateName    = 100
	maxPromptTemplateContent = 50000
)

// ListPromptTemplates returns the global prompt templates.
// GET /api/prompt-templates
func (s *Server) ListPromptTemplates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	templates := s.Config.Get().PromptTemplates
	if templates == nil {
		templates = []config.PromptTemplate{}
	}
	writeJSON(w, http.StatusOK, templates)
}

// ReplacePromptTemplates replaces the whole template list. The client owns
// ordering; names must be unique (case-insensitive).
// PUT /api/prompt-templates  [{name, content}, ...]
func (s *Server) ReplacePromptTemplates(w http.ResponseWriter, r *http.Request) {
	var req []config.PromptTemplate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req) > maxPromptTemplates {
		writeError(w, http.StatusBadRequest, "too many templates")
		return
	}
	seen := make(map[string]bool, len(req))
	cleaned := make([]config.PromptTemplate, 0, len(req))
	for _, t := range req {
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" || strings.TrimSpace(t.Content) == "" {
			writeError(w, http.StatusBadRequest, "template name and content are required")
			return
		}
		if utf8.RuneCountInString(t.Name) > maxPromptTemplateName {
			writeError(w, http.StatusBadRequest, "template name too long")
			return
		}
		if utf8.RuneCountInString(t.Content) > maxPromptTemplateContent {
			writeError(w, http.StatusBadRequest, "template content too long")
			return
		}
		key := strings.ToLower(t.Name)
		if seen[key] {
			writeError(w, http.StatusBadRequest, "duplicate template name: "+t.Name)
			return
		}
		seen[key] = true
		cleaned = append(cleaned, t)
	}
	if err := s.Config.Update(func(cfg *config.Config) {
		cfg.PromptTemplates = cleaned
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist templates")
		return
	}
	writeJSON(w, http.StatusOK, cleaned)
}
