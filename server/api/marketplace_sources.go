package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/esonic/nexbox/store"
)

// ---------------------------------------------------------------------------
// Marketplace sources (Add Marketplace)
// ---------------------------------------------------------------------------

type AddMarketplaceSourceRequest struct {
	Input string `json:"input"` // local path or git URL (auto-detected)
}

// hasValidPluginContent checks whether a directory contains any recognized plugin format.
func hasValidPluginContent(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "marketplace.json")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err == nil {
		return true
	}
	// Also accept repos containing SKILL.md files (e.g., anthropics/skills).
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && info.Name() == "SKILL.md" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// isGitURL returns true if the input looks like a git repository URL.
func isGitURL(input string) bool {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "git@") {
		return true
	}
	if strings.HasPrefix(input, "https://") || strings.HasPrefix(input, "http://") {
		// Ends with .git, or contains a known git host
		if strings.HasSuffix(input, ".git") {
			return true
		}
		lower := strings.ToLower(input)
		if strings.Contains(lower, "github.com/") || strings.Contains(lower, "gitlab.com/") || strings.Contains(lower, "gitlab.") {
			return true
		}
	}
	return false
}

// repoNameFromURL extracts a directory name from a git URL.
func repoNameFromURL(gitURL string) string {
	gitURL = strings.TrimSpace(gitURL)
	// git@github.com:org/repo.git → repo
	if idx := strings.LastIndex(gitURL, "/"); idx >= 0 {
		name := gitURL[idx+1:]
		name = strings.TrimSuffix(name, ".git")
		return name
	}
	if idx := strings.LastIndex(gitURL, ":"); idx >= 0 {
		name := gitURL[idx+1:]
		name = strings.TrimSuffix(name, ".git")
		if slash := strings.LastIndex(name, "/"); slash >= 0 {
			name = name[slash+1:]
		}
		return name
	}
	return "marketplace-source"
}

func (s *Server) AddMarketplaceSource(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	var req AddMarketplaceSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		writeError(w, http.StatusBadRequest, "input is required")
		return
	}

	mpDir := s.marketplaceDir()
	if mpDir == "" {
		writeError(w, http.StatusInternalServerError, "marketplace directory not configured")
		return
	}
	if err := os.MkdirAll(mpDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "create marketplace dir: "+err.Error())
		return
	}

	hash := sha256.Sum256([]byte(input))
	srcID := hex.EncodeToString(hash[:8])

	if isGitURL(input) {
		repoName := repoNameFromURL(input)
		destDir := filepath.Join(mpDir, repoName)
		if _, err := os.Stat(destDir); err == nil {
			writeError(w, http.StatusConflict, fmt.Sprintf("directory %s already exists in marketplace", repoName))
			return
		}
		cmd := exec.Command("git", "clone", "--depth", "1", input, destDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("git clone failed: %s\n%s", err, output))
			return
		}

		// Validate: must contain at least one recognized plugin format.
		if !hasValidPluginContent(destDir) {
			_ = os.RemoveAll(destDir)
			writeError(w, http.StatusBadRequest, "repository does not contain any recognized format (.claude-plugin/marketplace.json, .claude-plugin/plugin.json, or subdirectories with SKILL.md)")
			return
		}

		src := store.MarketplaceSource{
			ID:      srcID,
			Type:    "git",
			URL:     input,
			Path:    destDir,
			Name:    repoName,
			AddedAt: store.UnixTime(time.Now()),
		}
		if err := s.LocalStore.UpsertMarketplaceSource(src); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[marketplace] added git source: %s → %s", input, destDir)
		writeJSON(w, http.StatusCreated, src)
	} else {
		// Local directory
		absPath, err := filepath.Abs(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid path: "+err.Error())
			return
		}
		info, err := os.Stat(absPath)
		if err != nil || !info.IsDir() {
			writeError(w, http.StatusBadRequest, "path is not a valid directory")
			return
		}

		// Validate: must contain at least one recognized plugin format.
		if !hasValidPluginContent(absPath) {
			writeError(w, http.StatusBadRequest, "directory does not contain any recognized format (.claude-plugin/marketplace.json, .claude-plugin/plugin.json, or subdirectories with SKILL.md)")
			return
		}

		// Symlink into marketplace dir
		linkName := filepath.Base(absPath)
		linkPath := filepath.Join(mpDir, linkName)
		if _, err := os.Lstat(linkPath); err == nil {
			writeError(w, http.StatusConflict, fmt.Sprintf("directory %s already exists in marketplace", linkName))
			return
		}
		if err := os.Symlink(absPath, linkPath); err != nil {
			writeError(w, http.StatusInternalServerError, "symlink: "+err.Error())
			return
		}

		src := store.MarketplaceSource{
			ID:      srcID,
			Type:    "local",
			URL:     "",
			Path:    absPath,
			Name:    linkName,
			AddedAt: store.UnixTime(time.Now()),
		}
		if err := s.LocalStore.UpsertMarketplaceSource(src); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[marketplace] added local source: %s → %s", absPath, linkPath)
		writeJSON(w, http.StatusCreated, src)
	}
}

func (s *Server) ListMarketplaceSources(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeJSON(w, http.StatusOK, []store.MarketplaceSource{})
		return
	}
	sources, err := s.LocalStore.ListMarketplaceSources()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (s *Server) RemoveMarketplaceSource(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	id := chi.URLParam(r, "id")

	// Look up source before deleting so we can clean up the filesystem.
	src, _ := s.LocalStore.GetMarketplaceSource(id)

	if err := s.LocalStore.DeleteMarketplaceSource(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Clean up filesystem: remove symlink (local) or cloned directory (git).
	if src != nil && src.Name != "" {
		mpDir := s.marketplaceDir()
		linkOrDir := filepath.Join(mpDir, src.Name)
		if fi, err := os.Lstat(linkOrDir); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(linkOrDir)
				log.Printf("[marketplace] removed symlink: %s", linkOrDir)
			} else if fi.IsDir() {
				_ = os.RemoveAll(linkOrDir)
				log.Printf("[marketplace] removed directory: %s", linkOrDir)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Startup: clone official marketplace repos
// ---------------------------------------------------------------------------

// CloneOfficialMarketplaceRepos clones official repos into dataDir/marketplace asynchronously
// and registers each as a MarketplaceSource in the store so they appear in the UI.
func CloneOfficialMarketplaceRepos(dataDir string, ls *store.SQLiteStore) {
	mpDir := filepath.Join(dataDir, "marketplace")
	if err := os.MkdirAll(mpDir, 0755); err != nil {
		log.Printf("[marketplace] failed to create marketplace dir: %v", err)
		return
	}

	repos := []struct {
		url  string
		name string
	}{
		{"git@gitlab.okg.com:nexbox/plugin-marketplace.git", "sre-plugin-marketplace"},
	}

	for _, repo := range repos {
		dest := filepath.Join(mpDir, repo.name)
		if _, err := os.Stat(dest); err == nil {
			continue // already exists
		}
		log.Printf("[marketplace] cloning %s → %s", repo.url, dest)
		cmd := exec.Command("git", "clone", "--depth", "1", repo.url, dest)
		if output, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[marketplace] clone %s failed: %v\n%s", repo.url, err, output)
		} else {
			log.Printf("[marketplace] cloned %s", repo.name)
			if ls != nil {
				hash := sha256.Sum256([]byte(repo.url))
				src := store.MarketplaceSource{
					ID:      hex.EncodeToString(hash[:8]),
					Type:    "git",
					URL:     repo.url,
					Path:    dest,
					Name:    repo.name,
					AddedAt: store.UnixTime(time.Now()),
				}
				if err := ls.UpsertMarketplaceSource(src); err != nil {
					log.Printf("[marketplace] failed to register source %s: %v", repo.name, err)
				}
			}
		}
	}
}
