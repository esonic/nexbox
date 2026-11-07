//go:build unix

package api

import (
	"github.com/esonic/nexbox/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestResolveFileLinkRejectsFIFOWithoutBlocking(t *testing.T) {
	s := newRelayServer(t)
	root := t.TempDir()
	path := filepath.Join(root, "pipe.txt")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	project, err := s.Store.CreateProject(store.CreateProjectRequest{Name: "files", GitRepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRecorder()
		NewRouter(s).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/file-resolve?path=pipe.txt", nil))
		done <- r.Code
	}()
	select {
	case status := <-done:
		if status != 400 {
			t.Fatalf("status %d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO resolution blocked")
	}
}
