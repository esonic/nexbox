package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// ---------------------------------------------------------------------------
// Local skill install/uninstall
// ---------------------------------------------------------------------------

type InstallSkillRequest struct {
	Name        string `json:"name"`
	PackageID   string `json:"package_id"`
	Version     string `json:"version"`
	InstallPath string `json:"install_path"`
	ProjectID   string `json:"project_id"`
}

func (s *Server) ListInstalledSkills(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeJSON(w, http.StatusOK, []store.InstalledSkill{})
		return
	}
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		projectID = "*"
	}
	skills, err := s.LocalStore.ListInstalledSkills(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

func (s *Server) InstallSkill(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	var req InstallSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	var installDir string
	if req.ProjectID == "" {
		// Global install: store the source path directly, no intermediate copy.
		installDir = req.InstallPath
	} else {
		// Per-project install: symlink into the project's .claude/skills/ directory.
		destDir, err := s.skillInstallDir(req.Name, req.ProjectID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.InstallPath != "" {
			if err := symlinkDir(req.InstallPath, destDir); err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("link skill: %v", err))
				return
			}
		}
		installDir = destDir
	}

	skill := store.InstalledSkill{
		Name:        req.Name,
		ProjectID:   req.ProjectID,
		Version:     req.Version,
		InstallPath: installDir,
		InstalledAt: store.UnixTime(time.Now()),
	}
	if err := s.LocalStore.UpsertInstalledSkill(skill); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if req.ProjectID == "" {
		s.copyGlobalSkillToProjects(req.Name, installDir)
	} else {
		project, _ := s.getProjectWithLocalPath(req.ProjectID)
		if project != nil && project.GitRepoPath != "" {
			ensureAgentsSymlink(project.GitRepoPath)
		}
	}

	if s.MarketplaceStats != nil {
		s.MarketplaceStats.RecordInstall(req.Name, "skill")
	}
	log.Printf("[marketplace] installed skill: %s → %s", req.Name, installDir)
	writeJSON(w, http.StatusOK, skill)
}

func (s *Server) UninstallSkill(w http.ResponseWriter, r *http.Request) {
	if s.LocalStore == nil {
		writeError(w, http.StatusServiceUnavailable, "local store not available")
		return
	}
	name := chi.URLParam(r, "name")
	projectID := r.URL.Query().Get("project_id")

	// Collect install paths to clean up from disk.
	var pathsToRemove []string
	if projectID == "" {
		// Global uninstall: remove the per-project symlinks propagated to each project.
		// The global DB entry's InstallPath is the marketplace source — don't delete it.
		for _, repoPath := range s.allGitRepoPaths() {
			pathsToRemove = append(pathsToRemove, filepath.Join(repoPath, ".claude", "skills", name))
		}
	} else {
		// Per-project uninstall: remove the installed directory/symlink.
		if installDir, err := s.skillInstallDir(name, projectID); err == nil && installDir != "" {
			pathsToRemove = append(pathsToRemove, installDir)
		}
	}

	var delErr error
	if projectID == "" {
		delErr = s.LocalStore.DeleteInstalledSkillByName(name)
	} else {
		delErr = s.LocalStore.DeleteInstalledSkill(name, projectID)
	}
	if delErr != nil {
		writeError(w, http.StatusInternalServerError, delErr.Error())
		return
	}

	// Remove installed directories / symlinks from disk.
	seen := make(map[string]bool)
	for _, p := range pathsToRemove {
		if seen[p] {
			continue
		}
		seen[p] = true
		if err := removeInstallDir(p); err != nil {
			log.Printf("[marketplace] warning: failed to remove skill dir %s: %v", p, err)
		}
	}

	log.Printf("[marketplace] uninstalled skill: %s (project=%q)", name, projectID)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Skill helpers
// ---------------------------------------------------------------------------

func (s *Server) skillInstallDir(name, projectID string) (string, error) {
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	if project.GitRepoPath == "" {
		return "", fmt.Errorf("project has no git repo path")
	}
	return filepath.Join(project.GitRepoPath, ".claude", "skills", name), nil
}

func (s *Server) copyGlobalSkillsToProject(gitRepoPath string) {
	globalSkills, err := s.LocalStore.ListInstalledSkills("")
	if err != nil {
		log.Printf("[marketplace] failed to load global skills for new project: %v", err)
		return
	}
	for _, skill := range globalSkills {
		if skill.InstallPath == "" {
			continue
		}
		dest := filepath.Join(gitRepoPath, ".claude", "skills", skill.Name)
		if err := symlinkDir(skill.InstallPath, dest); err != nil {
			log.Printf("[marketplace] failed to link global skill %s to new project: %v", skill.Name, err)
		}
	}
}

func ensureAgentsSymlink(repoPath string) {
	link := filepath.Join(repoPath, ".agents")
	info, err := os.Lstat(link)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(link)
			if target == ".claude" {
				return
			}
		}
		return
	}
	if !os.IsNotExist(err) {
		return
	}
	if err := os.Symlink(".claude", link); err != nil {
		log.Printf("[marketplace] ensureAgentsSymlink: %v", err)
	}
}

func (s *Server) copyGlobalSkillToProjects(skillName, srcDir string) {
	var wg sync.WaitGroup
	for _, repoPath := range s.allGitRepoPaths() {
		wg.Add(1)
		go func(rp string) {
			defer wg.Done()
			dest := filepath.Join(rp, ".claude", "skills", skillName)
			if err := symlinkDir(srcDir, dest); err != nil {
				log.Printf("[marketplace] failed to link global skill %s to %s: %v", skillName, rp, err)
			} else {
				ensureAgentsSymlink(rp)
			}
		}(repoPath)
	}
	wg.Wait()
}
