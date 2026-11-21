package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/esonic/nexbox/store"
)

func TestPDFPreviewMetadata(t *testing.T) {
	s := newRelayServer(t)
	root := t.TempDir()
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "pdf", GitRepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		size   int
		status int
	}{
		{"small.pdf", 100, http.StatusOK},
		{"large.PDF", 3 * 1024 * 1024, http.StatusOK},
		{"large.txt", 3 * 1024 * 1024, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := "%PDF-1.4\n" + strings.Repeat("x", tc.size)
			if err := os.WriteFile(filepath.Join(root, tc.name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			NewRouter(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/file-content?path="+tc.name, nil))
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if tc.status != http.StatusOK {
				return
			}
			var body struct {
				Binary  bool   `json:"binary"`
				Size    int    `json:"size"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !body.Binary || body.Size != len(content) || body.Content != "" {
				t.Fatalf("unexpected PDF metadata: %+v", body)
			}
		})
	}
}
