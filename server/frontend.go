package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:webdist
var webDistFS embed.FS

// embeddedSPAHandler returns an http.HandlerFunc that serves embedded static files
// and falls back to index.html for SPA client-side routing.
func embeddedSPAHandler() http.HandlerFunc {
	distFS, err := fs.Sub(webDistFS, "webdist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(distFS))

	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			http.NotFound(w, r)
			return
		}

		// Try to open the file
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(distFS, path); err != nil {
			// File not found, serve index.html for SPA routing
			indexData, err := fs.ReadFile(distFS, "index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexData)
			return
		}

		fileServer.ServeHTTP(w, r)
	}
}

// hasEmbeddedWeb checks if the embedded FS contains actual web build output
// (more than just the .gitkeep placeholder).
func hasEmbeddedWeb() bool {
	entries, err := fs.ReadDir(webDistFS, "webdist")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() != ".gitkeep" {
			return true
		}
	}
	return false
}
