package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/esonic/nexbox/config"
)

func TestServeConfiguredVerificationFile(t *testing.T) {
	publicWeb := config.PublicWebConfig{
		VerificationFiles: map[string]string{"provider-check.txt": "verification-value"},
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/provider-check.txt", nil)

	if !serveConfiguredPublicFile(rr, req, publicWeb) {
		t.Fatal("configured verification file was not handled")
	}
	if got := rr.Body.String(); got != "verification-value" {
		t.Fatalf("body = %q", got)
	}
}

func TestServeConfiguredAssetLinks(t *testing.T) {
	publicWeb := config.PublicWebConfig{AndroidAssetLinks: &config.AndroidAssetLinksConfig{
		PackageName:            "example.app",
		SHA256CertFingerprints: []string{"AA:BB"},
	}}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/assetlinks.json", nil)

	if !serveConfiguredPublicFile(rr, req, publicWeb) {
		t.Fatal("configured asset links were not handled")
	}
	var payload []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	target := payload[0]["target"].(map[string]any)
	if got := target["package_name"]; got != "example.app" {
		t.Fatalf("package_name = %v", got)
	}
}

func TestEmbeddedSPAHandlerUsesConfigManager(t *testing.T) {
	manager, err := config.NewManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(func(cfg *config.Config) {
		cfg.PublicWeb.VerificationFiles = map[string]string{"check.txt": "configured"}
	}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	embeddedSPAHandler(manager).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/check.txt", nil))
	if got := rr.Body.String(); got != "configured" {
		t.Fatalf("body = %q", got)
	}
}
