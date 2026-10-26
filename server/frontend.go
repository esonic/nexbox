package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/esonic/nexbox/config"
)

//go:embed all:webdist
var webDistFS embed.FS

func init() {
	// The PWA manifest requires application/manifest+json so browsers
	// (and TWA verification) recognise it; Go's default MIME table omits it.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// embeddedSPAHandler returns an http.HandlerFunc that serves embedded static files
// and falls back to index.html for SPA client-side routing.
func embeddedSPAHandler(cfg *config.Manager) http.HandlerFunc {
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
		if serveConfiguredPublicFile(w, r, cfg.Get().PublicWeb) {
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

func serveConfiguredPublicFile(w http.ResponseWriter, r *http.Request, publicWeb config.PublicWebConfig) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	if r.URL.Path == "/.well-known/assetlinks.json" && publicWeb.AndroidAssetLinks != nil {
		links := publicWeb.AndroidAssetLinks
		if strings.TrimSpace(links.PackageName) == "" || len(links.SHA256CertFingerprints) == 0 {
			return false
		}
		payload := []map[string]any{{
			"relation": []string{"delegate_permission/common.handle_all_urls"},
			"target": map[string]any{
				"namespace":                "android_app",
				"package_name":             links.PackageName,
				"sha256_cert_fingerprints": links.SHA256CertFingerprints,
			},
		}}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(payload)
		}
		return true
	}

	filename := strings.TrimPrefix(r.URL.Path, "/")
	if filename == "" || path.Base(filename) != filename {
		return false
	}
	value, ok := publicWeb.VerificationFiles[filename]
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(value))
	}
	return true
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
