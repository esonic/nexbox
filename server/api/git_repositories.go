package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/esonic/nexbox/gitrepo"
	"github.com/go-chi/chi/v5"
)

var fallbackGitRepositoryResolver = gitrepo.NewResolver(gitrepo.DefaultOptions())

type effectiveWorkspace struct {
	Root      string
	Mode      string
	ProjectID string
	SessionID string
}

func (s *Server) repositoryResolver() *gitrepo.Resolver {
	if s.GitRepositories != nil {
		return s.GitRepositories
	}
	return fallbackGitRepositoryResolver
}

func (s *Server) resolveEffectiveWorkspace(r *http.Request, projectID string) (effectiveWorkspace, error) {
	project, err := s.getProjectWithLocalPath(projectID)
	if err != nil || project == nil {
		return effectiveWorkspace{}, fmt.Errorf("project not found")
	}
	if project.GitRepoPath == "" {
		return effectiveWorkspace{}, fmt.Errorf("project has no working directory configured")
	}
	workspace := effectiveWorkspace{Root: project.GitRepoPath, Mode: "local", ProjectID: projectID}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		return workspace, nil
	}
	session, err := s.Store.GetSession(sessionID)
	if err != nil || session == nil {
		return effectiveWorkspace{}, fmt.Errorf("session not found")
	}
	if session.ProjectID != projectID {
		return effectiveWorkspace{}, fmt.Errorf("session does not belong to this project")
	}
	workspace.SessionID = sessionID
	if session.WorktreeMode == "worktree" && session.WorktreePath != "" {
		workspace.Root = session.WorktreePath
		if effectiveRoot, normalizeErr := projectPathInWorktree(session.WorktreePath, project.GitRepoPath); normalizeErr == nil {
			workspace.Root = effectiveRoot
		} else {
			log.Printf("[git-discovery] Failed to normalize worktree path for session %s: %v", sessionID, normalizeErr)
		}
		workspace.Mode = "worktree"
	}
	return workspace, nil
}

func (s *Server) resolveRepositoryContext(r *http.Request, projectID string) (gitrepo.Context, error) {
	workspace, err := s.resolveEffectiveWorkspace(r, projectID)
	if err != nil {
		return gitrepo.Context{}, err
	}
	return s.repositoryResolver().Resolve(r.Context(), workspace.Root, r.URL.Query().Get("repository_id"))
}

func (s *Server) resolveRepositoryFileContext(r *http.Request, projectID, path string) (gitrepo.Context, string, error) {
	if r.URL.Query().Get("repository_id") != "" {
		repositoryContext, err := s.resolveRepositoryContext(r, projectID)
		if err != nil {
			return gitrepo.Context{}, "", err
		}
		clean, err := gitrepo.ValidateRelativePath(path)
		return repositoryContext, clean, err
	}
	clean, err := gitrepo.ValidateRelativePath(path)
	if err != nil {
		return gitrepo.Context{}, "", err
	}
	workspace, err := s.resolveEffectiveWorkspace(r, projectID)
	if err != nil {
		return gitrepo.Context{}, "", err
	}
	discovery, err := s.repositoryResolver().Discover(r.Context(), workspace.Root, false)
	if err != nil {
		return gitrepo.Context{}, "", err
	}
	type candidate struct {
		repository  gitrepo.Repository
		path        string
		rank        int
		specificity int
	}
	var selected *candidate
	for _, repository := range discovery.Repositories {
		repositoryPath := ""
		rank := 0
		specificity := 0
		for _, repositoryAlias := range repository.Aliases {
			alias := strings.TrimSuffix(repositoryAlias, "/")
			if clean == alias || !strings.HasPrefix(clean, alias+"/") {
				continue
			}
			remainder := strings.TrimPrefix(clean, alias+"/")
			prefix := repository.AliasPrefixes[repositoryAlias]
			mapped := remainder
			if prefix != "" {
				mapped = filepath.ToSlash(filepath.Join(filepath.FromSlash(prefix), filepath.FromSlash(remainder)))
			}
			if len(alias) > specificity {
				repositoryPath = mapped
				specificity = len(alias)
			}
		}
		if repositoryPath != "" {
			// A nested or symlinked repository owns files below its workspace
			// alias even when an outer repository also contains that alias.
			// Keep this as an explicit rank instead of a path-length offset so
			// unusually long absolute workspace paths cannot reverse ownership.
			rank = 1
		} else if repository.Relation == gitrepo.RelationWorkspace || repository.Relation == gitrepo.RelationAncestor {
			canonicalWorkspace, evalErr := filepath.EvalSymlinks(workspace.Root)
			if evalErr != nil {
				continue
			}
			absolute := filepath.Join(canonicalWorkspace, filepath.FromSlash(clean))
			relative, relErr := filepath.Rel(repository.CanonicalRoot, absolute)
			if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			repositoryPath = filepath.ToSlash(relative)
			specificity = len(repository.CanonicalRoot)
		} else {
			continue
		}
		if selected == nil || rank > selected.rank || (rank == selected.rank && specificity > selected.specificity) {
			copy := candidate{repository: repository, path: repositoryPath, rank: rank, specificity: specificity}
			selected = &copy
		}
	}
	if selected == nil {
		return gitrepo.Context{}, "", &gitrepo.Error{Code: gitrepo.CodeNotFound, Err: fmt.Errorf("file is not owned by a discovered Git repository")}
	}
	repositoryContext, err := s.repositoryResolver().Resolve(r.Context(), workspace.Root, selected.repository.ID)
	if err != nil {
		return gitrepo.Context{}, "", err
	}
	return repositoryContext, selected.path, nil
}

func writeRepositoryError(w http.ResponseWriter, err error) {
	code := gitrepo.ErrorCode(err)
	status := http.StatusBadRequest
	switch code {
	case gitrepo.CodeNotFound:
		status = http.StatusNotFound
	case gitrepo.CodeSelectionRequired, gitrepo.CodeStale, gitrepo.CodeBusy:
		status = http.StatusConflict
	case gitrepo.CodeInvalidPath:
		status = http.StatusBadRequest
	case gitrepo.CodeUnsupported:
		status = http.StatusUnprocessableEntity
	}
	if code == "" {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, status, map[string]string{"error": err.Error(), "code": code})
}

type gitRepositoryResponse struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	DisplayPath      string                `json:"display_path"`
	Relation         gitrepo.Relation      `json:"relation"`
	IsSymlink        bool                  `json:"is_symlink"`
	Shared           bool                  `json:"shared"`
	OutsideWorkspace bool                  `json:"outside_workspace"`
	Aliases          []string              `json:"aliases"`
	Branch           string                `json:"branch"`
	Detached         bool                  `json:"detached"`
	RemoteName       string                `json:"remote_name,omitempty"`
	RemoteURL        string                `json:"remote_url,omitempty"`
	Status           gitrepo.StatusSummary `json:"status"`
}

type repositoryRuntime struct {
	status    *gitrepo.Status
	files     []GitDiffFile
	remoteURL string
	err       error
}

func collectRepositoryRuntimes(ctx context.Context, resolver *gitrepo.Resolver, workspaceRoot string, repositories []gitrepo.Repository, includeDiffStats, refreshStatus, includeRemote bool) []repositoryRuntime {
	results := make([]repositoryRuntime, len(repositories))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := 4
	if len(repositories) < workers {
		workers = len(repositories)
	}
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				selected, err := resolver.Resolve(ctx, workspaceRoot, repositories[index].ID)
				if err != nil {
					results[index].err = err
					continue
				}
				status, err := resolver.Status(ctx, selected.Repository, refreshStatus)
				results[index].status = status
				results[index].err = err
				if includeRemote {
					results[index].remoteURL = gitrepo.ReadRemoteURL(ctx, selected.Repository.CanonicalRoot)
				}
				if err == nil && includeDiffStats {
					files, diffErr := runGitDiffStatsWithStatus(ctx, selected.Repository.CanonicalRoot, status)
					results[index].files = files
					if diffErr != nil {
						results[index].err = diffErr
					}
				}
			}
		}()
	}
	for index := range repositories {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return results
}

// GetGitRepositories discovers repositories reachable from the project's effective workspace.
func (s *Server) GetGitRepositories(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	projectID := chi.URLParam(r, "projectId")
	workspace, err := s.resolveEffectiveWorkspace(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	refresh := r.URL.Query().Get("refresh") == "true"
	discovery, err := s.repositoryResolver().Discover(r.Context(), workspace.Root, refresh)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("[git-discovery] project=%s session=%s repos=%d duration=%s truncated=%t", projectID, workspace.SessionID, len(discovery.Repositories), time.Since(startedAt).Round(time.Millisecond), discovery.Truncated)
	runtimes := collectRepositoryRuntimes(r.Context(), s.repositoryResolver(), workspace.Root, discovery.Repositories, false, refresh, true)
	repositories := make([]gitRepositoryResponse, 0, len(discovery.Repositories))
	dirtyRepositories := 0
	for index, repo := range discovery.Repositories {
		item := gitRepositoryResponse{
			ID: repo.ID, Name: repo.Name, DisplayPath: repo.DisplayPath, Relation: repo.Relation,
			IsSymlink: repo.IsSymlink, OutsideWorkspace: repo.OutsideWorkspace,
			Aliases: append([]string(nil), repo.Aliases...),
			Shared:  workspace.Mode == "worktree" && repo.IsSymlink && repo.OutsideWorkspace,
		}
		if runtimes[index].status != nil {
			item.Status = runtimes[index].status.Summary()
			item.Branch = item.Status.Branch
			item.Detached = item.Status.Detached
			if item.Status.Total > 0 {
				dirtyRepositories++
			}
		} else if runtimes[index].err != nil {
			discovery.Warnings = append(discovery.Warnings, fmt.Sprintf("failed to read status for %s", repo.DisplayPath))
		}
		remoteURL := runtimes[index].remoteURL
		if remoteURL != "" {
			item.RemoteName = "origin"
			item.RemoteURL = redactRemoteURL(remoteURL)
		}
		repositories = append(repositories, item)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_mode":         workspace.Mode,
		"default_repository_id":  discovery.DefaultRepositoryID,
		"repositories":           repositories,
		"dirty_repository_count": dirtyRepositories,
		"truncated":              discovery.Truncated,
		"warnings":               discovery.Warnings,
	})
}

type gitWorkspaceFileStatus struct {
	RepositoryID   string `json:"repository_id"`
	RepositoryPath string `json:"repository_path"`
	WorkspacePath  string `json:"workspace_path"`
	IndexStatus    string `json:"index_status,omitempty"`
	WorktreeStatus string `json:"worktree_status,omitempty"`
}

// GetGitWorkspaceStatus returns aggregate change information across all discovered repositories.
func (s *Server) GetGitWorkspaceStatus(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	workspace, err := s.resolveEffectiveWorkspace(r, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	discovery, err := s.repositoryResolver().Discover(r.Context(), workspace.Root, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	includeFiles := r.URL.Query().Get("include_files") == "true"
	runtimes := collectRepositoryRuntimes(r.Context(), s.repositoryResolver(), workspace.Root, discovery.Repositories, true, false, false)
	type repoSummary struct {
		RepositoryID string `json:"repository_id"`
		TotalFiles   int    `json:"total_files"`
		Insertions   int    `json:"insertions"`
		Deletions    int    `json:"deletions"`
	}
	repoSummaries := make([]repoSummary, 0, len(discovery.Repositories))
	workspaceFiles := []gitWorkspaceFileStatus{}
	totalFiles, insertions, deletions, dirtyRepositories := 0, 0, 0, 0
	for index, repo := range discovery.Repositories {
		runtime := runtimes[index]
		if runtime.status == nil {
			continue
		}
		summary := runtime.status.Summary()
		if summary.Total > 0 {
			dirtyRepositories++
		}
		repoInsertions, repoDeletions := 0, 0
		for _, file := range runtime.files {
			repoInsertions += file.Insertions
			repoDeletions += file.Deletions
		}
		totalFiles += summary.Total
		insertions += repoInsertions
		deletions += repoDeletions
		repoSummaries = append(repoSummaries, repoSummary{RepositoryID: repo.ID, TotalFiles: summary.Total, Insertions: repoInsertions, Deletions: repoDeletions})
		if !includeFiles {
			continue
		}
		byPath := make(map[string]*gitWorkspaceFileStatus)
		add := func(file gitrepo.FileStatus, staged bool) {
			workspacePath, ok := gitrepo.WorkspacePath(workspace.Root, repo, file.Path)
			if !ok {
				return
			}
			item := byPath[file.Path]
			if item == nil {
				item = &gitWorkspaceFileStatus{RepositoryID: repo.ID, RepositoryPath: file.Path, WorkspacePath: workspacePath}
				byPath[file.Path] = item
			}
			if staged {
				item.IndexStatus = file.Status
			} else {
				item.WorktreeStatus = file.Status
			}
		}
		for _, file := range runtime.status.Staged {
			add(file, true)
		}
		for _, file := range runtime.status.Unstaged {
			add(file, false)
		}
		for _, file := range runtime.status.Untracked {
			add(file, false)
		}
		for _, file := range byPath {
			workspaceFiles = append(workspaceFiles, *file)
		}
	}
	sort.Slice(workspaceFiles, func(i, j int) bool {
		if workspaceFiles[i].WorkspacePath != workspaceFiles[j].WorkspacePath {
			return workspaceFiles[i].WorkspacePath < workspaceFiles[j].WorkspacePath
		}
		return workspaceFiles[i].RepositoryID < workspaceFiles[j].RepositoryID
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary": map[string]int{
			"repository_count": len(discovery.Repositories), "dirty_repository_count": dirtyRepositories,
			"total_files": totalFiles, "insertions": insertions, "deletions": deletions,
		},
		"repositories": repoSummaries,
		"files":        workspaceFiles,
	})
}

func redactRemoteURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	return raw
}

func remoteHost(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	// SCP-like Git syntax: [user@]host:path/to/repository.git.
	prefix, _, found := strings.Cut(raw, ":")
	if !found || strings.ContainsAny(prefix, "/\\") {
		return ""
	}
	if _, host, hasUser := strings.Cut(prefix, "@"); hasUser {
		return host
	}
	return prefix
}
