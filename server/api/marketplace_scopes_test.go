package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/esonic/nexbox/store"
	"github.com/go-chi/chi/v5"
)

// Global and project scope changes must preserve the remaining installation,
// including skill/plugin files propagated into the project.
func TestUninstallMarketplaceScope(t *testing.T) {
	for _, kind := range []string{"mcp", "skill", "plugin"} {
		for _, target := range []string{"global", "project", "all"} {
			t.Run(kind+"/"+target, func(t *testing.T) {
				dataDir := t.TempDir()
				db, err := store.New(dataDir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				ls := store.NewSQLiteStore(db)
				srv := &Server{Store: ls, DataDir: dataDir}
				repo := t.TempDir()
				project, err := ls.CreateProject(store.CreateProjectRequest{Name: "selected", GitRepoPath: repo})
				if err != nil {
					t.Fatal(err)
				}
				source := t.TempDir()
				if err := os.WriteFile(filepath.Join(source, "content"), []byte("keep"), 0644); err != nil {
					t.Fatal(err)
				}
				name := "scope-test"
				var installPath string
				if kind != "mcp" {
					folder := "skills"
					if kind == "plugin" {
						folder = "plugins"
					}
					installPath = filepath.Join(repo, ".claude", folder, name)
					if err := symlinkDir(source, installPath); err != nil {
						t.Fatal(err)
					}
				}
				for _, scope := range []string{"", project.ID} {
					path := source
					if scope != "" {
						path = installPath
					}
					switch kind {
					case "mcp":
						err = ls.UpsertInstalledMCP(store.InstalledMCP{Name: name, MCPKey: name, ProjectID: scope, Manifest: "{}"})
					case "skill":
						err = ls.UpsertInstalledSkill(store.InstalledSkill{Name: name, ProjectID: scope, InstallPath: path})
					case "plugin":
						err = ls.UpsertInstalledPluginEntry(store.InstalledPluginEntry{Name: name, ProjectID: scope, InstallPath: path})
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				query := ""
				if target == "global" {
					query = "?project_id="
				}
				if target == "project" {
					query = "?project_id=" + project.ID
				}
				req := httptest.NewRequest(http.MethodDelete, "/installed/"+name+query, nil)
				route := chi.NewRouteContext()
				route.URLParams.Add("name", name)
				req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
				res := httptest.NewRecorder()
				var remaining []string
				switch kind {
				case "mcp":
					srv.UninstallMCP(res, req)
					entries, err := ls.ListInstalledMCPs("*")
					if err != nil {
						t.Fatal(err)
					}
					for _, e := range entries {
						remaining = append(remaining, e.ProjectID)
					}
				case "skill":
					srv.UninstallSkill(res, req)
					entries, err := ls.ListInstalledSkills("*")
					if err != nil {
						t.Fatal(err)
					}
					for _, e := range entries {
						remaining = append(remaining, e.ProjectID)
					}
				case "plugin":
					srv.UninstallPluginEntry(res, req)
					entries, err := ls.ListInstalledPluginEntries("*")
					if err != nil {
						t.Fatal(err)
					}
					for _, e := range entries {
						remaining = append(remaining, e.ProjectID)
					}
				}
				if res.Code != http.StatusNoContent {
					t.Fatalf("status %d: %s", res.Code, res.Body.String())
				}
				if target == "all" {
					if len(remaining) != 0 {
						t.Fatalf("remaining scopes: %v", remaining)
					}
					if installPath != "" {
						if _, err := os.Lstat(installPath); !os.IsNotExist(err) {
							t.Fatalf("installation still exists: %v", err)
						}
					}
				} else {
					expected := ""
					if target == "global" {
						expected = project.ID
					}
					if len(remaining) != 1 || remaining[0] != expected {
						t.Fatalf("remaining scopes: %v, want [%q]", remaining, expected)
					}
					if installPath != "" {
						if data, err := os.ReadFile(filepath.Join(installPath, "content")); err != nil || string(data) != "keep" {
							t.Fatalf("retained installation unusable: %s, %v", data, err)
						}
					}
				}
				if _, err := os.Stat(filepath.Join(source, "content")); err != nil {
					t.Fatalf("marketplace source removed: %v", err)
				}
			})
		}
	}
}
