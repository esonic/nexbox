package gitrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Relation string

const (
	RelationWorkspace Relation = "workspace"
	RelationAncestor  Relation = "ancestor"
	RelationNested    Relation = "nested"
	RelationSymlink   Relation = "symlink"
)

const (
	CodeNotFound          = "repository_not_found"
	CodeSelectionRequired = "repository_selection_required"
	CodeStale             = "repository_stale"
	CodeInvalidPath       = "invalid_repository_path"
	CodeBusy              = "repository_busy"
	CodeUnsupported       = "repository_operation_unsupported"
)

type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string {
	if e == nil || e.Err == nil {
		return e.Code
	}
	return e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

func ErrorCode(err error) string {
	var repoErr *Error
	if errors.As(err, &repoErr) {
		return repoErr.Code
	}
	return ""
}

type Repository struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	DisplayPath      string   `json:"display_path"`
	Relation         Relation `json:"relation"`
	IsSymlink        bool     `json:"is_symlink"`
	OutsideWorkspace bool     `json:"outside_workspace"`
	Aliases          []string `json:"aliases"`

	Root          string            `json:"-"`
	CanonicalRoot string            `json:"-"`
	GitDir        string            `json:"-"`
	CommonDir     string            `json:"-"`
	Anchor        string            `json:"-"`
	LinkedTarget  string            `json:"-"`
	AliasPrefixes map[string]string `json:"-"`
}

type DiscoveryResult struct {
	WorkspaceRoot       string       `json:"-"`
	Repositories        []Repository `json:"repositories"`
	DefaultRepositoryID string       `json:"default_repository_id"`
	Truncated           bool         `json:"truncated"`
	Warnings            []string     `json:"warnings"`
}

type Context struct {
	WorkspaceRoot string
	Repository    Repository
}

type Options struct {
	MaxDepth        int
	MaxSymlinkDepth int
	MaxDirectories  int
	MaxRepositories int
	ScanTimeout     time.Duration
	TopologyTTL     time.Duration
	StatusTTL       time.Duration
	IgnoredDirs     map[string]bool
}

func DefaultOptions() Options {
	return Options{
		MaxDepth:        8,
		MaxSymlinkDepth: 2,
		MaxDirectories:  10_000,
		MaxRepositories: 50,
		ScanTimeout:     3 * time.Second,
		TopologyTTL:     30 * time.Second,
		StatusTTL:       3 * time.Second,
		IgnoredDirs: map[string]bool{
			".git": true, "node_modules": true, "vendor": true,
			"dist": true, "build": true, "target": true, ".next": true,
			".venv": true, "__pycache__": true, ".idea": true,
			".vscode": true,
		},
	}
}

type cacheEntry struct {
	result    DiscoveryResult
	expiresAt time.Time
}

type statusCacheEntry struct {
	status    Status
	expiresAt time.Time
}

type mutationLock struct {
	token chan struct{}
	refs  int
}

const (
	maxTopologyCacheEntries = 256
	maxStatusCacheEntries   = 512
)

type Resolver struct {
	opts          Options
	mu            sync.Mutex
	cache         map[string]cacheEntry
	statusCache   map[string]statusCacheEntry
	statusEpoch   uint64
	mutationLocks map[string]*mutationLock
	now           func() time.Time
}

func NewResolver(opts Options) *Resolver {
	defaults := DefaultOptions()
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = defaults.MaxDepth
	}
	if opts.MaxSymlinkDepth <= 0 {
		opts.MaxSymlinkDepth = defaults.MaxSymlinkDepth
	}
	if opts.MaxDirectories <= 0 {
		opts.MaxDirectories = defaults.MaxDirectories
	}
	if opts.MaxRepositories <= 0 {
		opts.MaxRepositories = defaults.MaxRepositories
	}
	if opts.ScanTimeout <= 0 {
		opts.ScanTimeout = defaults.ScanTimeout
	}
	if opts.TopologyTTL <= 0 {
		opts.TopologyTTL = defaults.TopologyTTL
	}
	if opts.StatusTTL <= 0 {
		opts.StatusTTL = defaults.StatusTTL
	}
	if opts.IgnoredDirs == nil {
		opts.IgnoredDirs = defaults.IgnoredDirs
	}
	return &Resolver{
		opts: opts, cache: make(map[string]cacheEntry),
		statusCache: make(map[string]statusCacheEntry), mutationLocks: make(map[string]*mutationLock),
		now: time.Now,
	}
}

func (r *Resolver) Discover(ctx context.Context, workspaceRoot string, refresh bool) (DiscoveryResult, error) {
	root, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return DiscoveryResult{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return DiscoveryResult{}, fmt.Errorf("workspace root is not an accessible directory")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return DiscoveryResult{}, fmt.Errorf("resolve workspace symlinks: %w", err)
	}
	cacheKey := root + "\x00" + canonicalRoot
	now := r.currentTime()
	r.mu.Lock()
	r.pruneExpiredLocked(now)
	if !refresh {
		cached, ok := r.cache[cacheKey]
		if ok {
			r.mu.Unlock()
			return cloneDiscovery(cached.result), nil
		}
	}
	r.mu.Unlock()

	scanCtx, cancel := context.WithTimeout(ctx, r.opts.ScanTimeout)
	defer cancel()
	result, err := r.scan(scanCtx, root, canonicalRoot)
	if err != nil {
		return DiscoveryResult{}, err
	}
	if ctx.Err() != nil {
		return DiscoveryResult{}, ctx.Err()
	}
	now = r.currentTime()
	r.mu.Lock()
	r.pruneExpiredLocked(now)
	r.storeTopologyCacheLocked(cacheKey, cacheEntry{result: cloneDiscovery(result), expiresAt: now.Add(r.opts.TopologyTTL)})
	r.mu.Unlock()
	return result, nil
}

func cloneDiscovery(in DiscoveryResult) DiscoveryResult {
	out := in
	out.Repositories = append([]Repository(nil), in.Repositories...)
	for i := range out.Repositories {
		out.Repositories[i].Aliases = append([]string(nil), in.Repositories[i].Aliases...)
		out.Repositories[i].AliasPrefixes = cloneStringMap(in.Repositories[i].AliasPrefixes)
	}
	out.Warnings = append([]string(nil), in.Warnings...)
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func (r *Resolver) Resolve(ctx context.Context, workspaceRoot, repositoryID string) (Context, error) {
	result, err := r.Discover(ctx, workspaceRoot, false)
	if err != nil {
		return Context{}, err
	}
	var selected *Repository
	if repositoryID != "" {
		for i := range result.Repositories {
			if result.Repositories[i].ID == repositoryID {
				selected = &result.Repositories[i]
				break
			}
		}
		if selected == nil {
			return Context{}, &Error{Code: CodeNotFound, Err: errors.New("selected repository was not found in this workspace")}
		}
	} else {
		switch len(result.Repositories) {
		case 0:
			return Context{}, &Error{Code: CodeNotFound, Err: errors.New("no Git repository found in this workspace")}
		case 1:
			selected = &result.Repositories[0]
		default:
			for i := range result.Repositories {
				if result.Repositories[i].Relation == RelationWorkspace || result.Repositories[i].Relation == RelationAncestor {
					selected = &result.Repositories[i]
					break
				}
			}
			if selected == nil {
				return Context{}, &Error{Code: CodeSelectionRequired, Err: errors.New("multiple Git repositories found; repository_id is required")}
			}
		}
	}
	if selected == nil {
		return Context{}, &Error{Code: CodeNotFound, Err: errors.New("Git repository not found")}
	}
	if err := revalidate(ctx, result.WorkspaceRoot, *selected); err != nil {
		return Context{}, &Error{Code: CodeStale, Err: err}
	}
	return Context{WorkspaceRoot: result.WorkspaceRoot, Repository: *selected}, nil
}

func (r *Resolver) Refresh(ctx context.Context, workspaceRoot string) (DiscoveryResult, error) {
	return r.Discover(ctx, workspaceRoot, true)
}

// Status returns a short-lived status snapshot for a verified repository. The
// topology cache and status cache intentionally have separate lifetimes so the
// UI can poll status without rescanning the workspace tree.
func (r *Resolver) Status(ctx context.Context, repo Repository, refresh bool) (*Status, error) {
	now := r.currentTime()
	r.mu.Lock()
	r.pruneExpiredLocked(now)
	epoch := r.statusEpoch
	if !refresh {
		cached, ok := r.statusCache[repo.CanonicalRoot]
		if ok {
			r.mu.Unlock()
			status := cloneStatus(cached.status)
			return &status, nil
		}
	}
	r.mu.Unlock()
	status, err := ReadStatus(ctx, repo.CanonicalRoot)
	if err != nil {
		return nil, err
	}
	now = r.currentTime()
	r.mu.Lock()
	// A mutation may have invalidated the cache while git status was running.
	// Do not let that older in-flight read repopulate the cache afterwards.
	if r.statusEpoch == epoch {
		r.pruneExpiredLocked(now)
		r.storeStatusCacheLocked(repo.CanonicalRoot, statusCacheEntry{status: cloneStatus(*status), expiresAt: now.Add(r.opts.StatusTTL)})
	}
	r.mu.Unlock()
	return status, nil
}

func (r *Resolver) InvalidateStatus(canonicalRoot string) {
	r.mu.Lock()
	r.statusEpoch++
	delete(r.statusCache, canonicalRoot)
	r.mu.Unlock()
}

func cloneStatus(in Status) Status {
	out := in
	out.Staged = append([]FileStatus(nil), in.Staged...)
	out.Unstaged = append([]FileStatus(nil), in.Unstaged...)
	out.Untracked = append([]FileStatus(nil), in.Untracked...)
	return out
}

func (r *Resolver) currentTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Resolver) pruneExpiredLocked(now time.Time) {
	for key, entry := range r.cache {
		if !now.Before(entry.expiresAt) {
			delete(r.cache, key)
		}
	}
	for key, entry := range r.statusCache {
		if !now.Before(entry.expiresAt) {
			delete(r.statusCache, key)
		}
	}
}

func (r *Resolver) storeTopologyCacheLocked(key string, entry cacheEntry) {
	if _, exists := r.cache[key]; !exists && len(r.cache) >= maxTopologyCacheEntries {
		evictOldestTopologyCacheEntry(r.cache)
	}
	r.cache[key] = entry
}

func (r *Resolver) storeStatusCacheLocked(key string, entry statusCacheEntry) {
	if _, exists := r.statusCache[key]; !exists && len(r.statusCache) >= maxStatusCacheEntries {
		evictOldestStatusCacheEntry(r.statusCache)
	}
	r.statusCache[key] = entry
}

func evictOldestTopologyCacheEntry(cache map[string]cacheEntry) {
	var oldestKey string
	var oldestExpiry time.Time
	found := false
	for key, entry := range cache {
		if !found || entry.expiresAt.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = entry.expiresAt
			found = true
		}
	}
	if found {
		delete(cache, oldestKey)
	}
}

func evictOldestStatusCacheEntry(cache map[string]statusCacheEntry) {
	var oldestKey string
	var oldestExpiry time.Time
	found := false
	for key, entry := range cache {
		if !found || entry.expiresAt.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = entry.expiresAt
			found = true
		}
	}
	if found {
		delete(cache, oldestKey)
	}
}

// LockMutation serializes Git mutations by canonical repository root and then
// revalidates the selected repository while holding the lock. This closes the
// discovery-to-write race for moved directories and retargeted symlinks.
func (r *Resolver) LockMutation(ctx context.Context, selected Context) (func(), error) {
	key := selected.Repository.CanonicalRoot
	r.mu.Lock()
	lock := r.mutationLocks[key]
	if lock == nil {
		lock = &mutationLock{token: make(chan struct{}, 1)}
		lock.token <- struct{}{}
		r.mutationLocks[key] = lock
	}
	lock.refs++
	r.mu.Unlock()

	select {
	case <-ctx.Done():
		r.releaseMutationReference(key, lock, false)
		return nil, &Error{Code: CodeBusy, Err: fmt.Errorf("repository mutation canceled while waiting for lock: %w", ctx.Err())}
	case <-lock.token:
	}

	if err := revalidate(ctx, selected.WorkspaceRoot, selected.Repository); err != nil {
		r.releaseMutationReference(key, lock, true)
		return nil, &Error{Code: CodeStale, Err: err}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.statusEpoch++
			delete(r.statusCache, key)
			r.mu.Unlock()
			r.releaseMutationReference(key, lock, true)
		})
	}, nil
}

func (r *Resolver) releaseMutationReference(key string, lock *mutationLock, held bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if held {
		lock.token <- struct{}{}
	}
	lock.refs--
	if lock.refs == 0 && r.mutationLocks[key] == lock {
		delete(r.mutationLocks, key)
	}
}

func (r *Resolver) scan(ctx context.Context, workspaceRoot, canonicalWorkspace string) (DiscoveryResult, error) {
	result := DiscoveryResult{WorkspaceRoot: workspaceRoot, Repositories: []Repository{}, Warnings: []string{}}
	repositories := make(map[string]*Repository)

	add := func(verified verifiedRepository, relation Relation, alias, anchor string, symlink bool, aliasPrefix, linkedTarget string) {
		alias = normalizeDisplayPath(alias)
		aliasPrefix = normalizeRepositoryPrefix(aliasPrefix)
		if existing := repositories[verified.canonicalRoot]; existing != nil {
			if !contains(existing.Aliases, alias) {
				existing.Aliases = append(existing.Aliases, alias)
				sort.Strings(existing.Aliases)
			}
			if existing.AliasPrefixes == nil {
				existing.AliasPrefixes = make(map[string]string)
			}
			existing.AliasPrefixes[alias] = aliasPrefix
			if relationPriority(relation) < relationPriority(existing.Relation) {
				existing.Relation = relation
				existing.DisplayPath = alias
				existing.Anchor = anchor
				existing.IsSymlink = symlink
				existing.LinkedTarget = linkedTarget
			}
			return
		}
		repo := &Repository{
			ID:               repositoryID(verified.canonicalRoot),
			Name:             filepath.Base(verified.canonicalRoot),
			DisplayPath:      alias,
			Relation:         relation,
			IsSymlink:        symlink,
			OutsideWorkspace: !PathWithin(canonicalWorkspace, verified.canonicalRoot),
			Aliases:          []string{alias},
			Root:             verified.root,
			CanonicalRoot:    verified.canonicalRoot,
			GitDir:           verified.gitDir,
			CommonDir:        verified.commonDir,
			Anchor:           anchor,
			LinkedTarget:     linkedTarget,
			AliasPrefixes:    map[string]string{alias: aliasPrefix},
		}
		repositories[verified.canonicalRoot] = repo
	}

	if verified, err := verifyRepository(ctx, workspaceRoot); err == nil {
		relation := RelationAncestor
		alias, relErr := filepath.Rel(canonicalWorkspace, verified.canonicalRoot)
		if verified.canonicalRoot == canonicalWorkspace {
			relation = RelationWorkspace
			alias = "."
		} else if relErr != nil {
			alias = verified.canonicalRoot
		}
		add(verified, relation, alias, workspaceRoot, false, "", "")
	}

	directories := 0
	visited := make(map[string]bool)
	scanStopped := false
	directoryLimited := false
	repositoryLimited := false
	timedOut := false
	var walk func(string, int)
	walk = func(dir string, depth int) {
		if scanStopped {
			return
		}
		select {
		case <-ctx.Done():
			result.Truncated = true
			timedOut = true
			scanStopped = true
			return
		default:
		}
		if depth > r.opts.MaxDepth {
			return
		}
		if directories >= r.opts.MaxDirectories {
			result.Truncated = true
			directoryLimited = true
			scanStopped = true
			return
		}
		canonicalDir, err := filepath.EvalSymlinks(dir)
		if err != nil || visited[canonicalDir] {
			return
		}
		visited[canonicalDir] = true
		directories++

		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			if verified, verifyErr := verifyRepository(ctx, dir); verifyErr == nil && verified.canonicalRoot == canonicalDir {
				alias, _ := filepath.Rel(workspaceRoot, dir)
				relation := RelationNested
				if canonicalDir == canonicalWorkspace {
					relation = RelationWorkspace
					alias = "."
				}
				add(verified, relation, alias, dir, false, "", "")
			}
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if scanStopped {
				return
			}
			if len(repositories) >= r.opts.MaxRepositories {
				result.Truncated = true
				repositoryLimited = true
				scanStopped = true
				return
			}
			name := entry.Name()
			if r.opts.IgnoredDirs[name] {
				continue
			}
			child := filepath.Join(dir, name)
			if entry.Type()&os.ModeSymlink != 0 {
				if depth+1 > r.opts.MaxSymlinkDepth {
					continue
				}
				info, statErr := os.Stat(child)
				if statErr != nil {
					alias, _ := filepath.Rel(workspaceRoot, child)
					result.Warnings = append(result.Warnings, fmt.Sprintf("broken symlink skipped: %s", normalizeDisplayPath(alias)))
					continue
				}
				if !info.IsDir() {
					continue
				}
				target, evalErr := filepath.EvalSymlinks(child)
				if evalErr != nil {
					alias, _ := filepath.Rel(workspaceRoot, child)
					result.Warnings = append(result.Warnings, fmt.Sprintf("unresolvable symlink skipped: %s", normalizeDisplayPath(alias)))
					continue
				}
				verified, verifyErr := verifyRepository(ctx, target)
				if verifyErr != nil || !PathWithin(verified.canonicalRoot, target) {
					continue
				}
				prefix, prefixErr := filepath.Rel(verified.canonicalRoot, target)
				if prefixErr != nil || prefix == ".." || strings.HasPrefix(prefix, ".."+string(filepath.Separator)) {
					continue
				}
				alias, _ := filepath.Rel(workspaceRoot, child)
				add(verified, RelationSymlink, alias, child, true, prefix, target)
				continue
			}
			if entry.IsDir() {
				walk(child, depth+1)
			}
		}
	}
	walk(workspaceRoot, 0)

	if timedOut || ctx.Err() != nil {
		result.Warnings = append(result.Warnings, "repository scan timed out; showing partial results")
	}
	if directoryLimited {
		result.Warnings = append(result.Warnings, fmt.Sprintf("repository scan directory limit reached (max %d); showing partial results", r.opts.MaxDirectories))
	}
	if repositoryLimited {
		result.Warnings = append(result.Warnings, fmt.Sprintf("repository count limit reached (max %d); showing partial results", r.opts.MaxRepositories))
	}
	for _, repo := range repositories {
		result.Repositories = append(result.Repositories, *repo)
		if repo.Relation == RelationWorkspace || repo.Relation == RelationAncestor {
			result.DefaultRepositoryID = repo.ID
		}
	}
	sort.Slice(result.Repositories, func(i, j int) bool {
		left, right := result.Repositories[i], result.Repositories[j]
		if relationPriority(left.Relation) != relationPriority(right.Relation) {
			return relationPriority(left.Relation) < relationPriority(right.Relation)
		}
		return left.DisplayPath < right.DisplayPath
	})
	if result.DefaultRepositoryID == "" && len(result.Repositories) > 0 {
		result.DefaultRepositoryID = result.Repositories[0].ID
	}
	return result, nil
}

type verifiedRepository struct {
	root          string
	canonicalRoot string
	gitDir        string
	commonDir     string
}

func verifyRepository(ctx context.Context, start string) (verifiedRepository, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", start, "rev-parse", "--is-inside-work-tree", "--path-format=absolute", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return verifiedRepository{}, fmt.Errorf("git rev-parse: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[0]) != "true" {
		return verifiedRepository{}, errors.New("not a Git working tree")
	}
	root := filepath.Clean(strings.TrimSpace(lines[1]))
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return verifiedRepository{}, fmt.Errorf("resolve repository root: %w", err)
	}
	gitDir, err := canonicalPath(strings.TrimSpace(lines[2]))
	if err != nil {
		return verifiedRepository{}, fmt.Errorf("resolve git dir: %w", err)
	}
	commonDir, err := canonicalPath(strings.TrimSpace(lines[3]))
	if err != nil {
		return verifiedRepository{}, fmt.Errorf("resolve git common dir: %w", err)
	}
	return verifiedRepository{root: root, canonicalRoot: canonicalRoot, gitDir: gitDir, commonDir: commonDir}, nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func revalidate(ctx context.Context, workspaceRoot string, repo Repository) error {
	var start string
	switch repo.Relation {
	case RelationWorkspace, RelationAncestor:
		start = workspaceRoot
	case RelationNested:
		start = repo.Anchor
		canonical, err := filepath.EvalSymlinks(start)
		if err != nil || canonical != repo.CanonicalRoot {
			return errors.New("selected repository directory changed")
		}
	case RelationSymlink:
		start = repo.Anchor
		info, err := os.Lstat(start)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return errors.New("selected repository symlink changed")
		}
		canonical, err := filepath.EvalSymlinks(start)
		expectedTarget := repo.LinkedTarget
		if expectedTarget == "" {
			expectedTarget = repo.CanonicalRoot
		}
		if err != nil || canonical != expectedTarget {
			return errors.New("selected repository symlink target changed")
		}
	default:
		return errors.New("unknown repository relation")
	}
	verified, err := verifyRepository(ctx, start)
	if err != nil || verified.canonicalRoot != repo.CanonicalRoot || verified.gitDir != repo.GitDir || verified.commonDir != repo.CommonDir {
		return errors.New("selected repository identity changed")
	}
	return nil
}

func repositoryID(canonicalRoot string) string {
	digest := sha256.Sum256([]byte(canonicalRoot))
	return "repo_" + hex.EncodeToString(digest[:])[:24]
}

func normalizeDisplayPath(path string) string {
	if path == "" || path == "." {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func normalizeRepositoryPrefix(path string) string {
	if path == "" || path == "." {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func relationPriority(relation Relation) int {
	switch relation {
	case RelationWorkspace:
		return 0
	case RelationAncestor:
		return 1
	case RelationNested:
		return 2
	case RelationSymlink:
		return 3
	default:
		return 4
	}
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func PathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func ValidateRelativePath(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || filepath.IsAbs(path) {
		return "", &Error{Code: CodeInvalidPath, Err: errors.New("invalid repository-relative path")}
	}
	path = filepath.FromSlash(path)
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", &Error{Code: CodeInvalidPath, Err: errors.New("invalid repository-relative path")}
	}
	return filepath.ToSlash(clean), nil
}

func ResolveFilePath(repo Repository, relativePath string) (string, error) {
	clean, err := ValidateRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	full := filepath.Join(repo.CanonicalRoot, filepath.FromSlash(clean))
	parent := filepath.Dir(full)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", &Error{Code: CodeInvalidPath, Err: fmt.Errorf("resolve parent directory: %w", err)}
	}
	if !PathWithin(repo.CanonicalRoot, resolvedParent) {
		return "", &Error{Code: CodeInvalidPath, Err: errors.New("path escapes repository root through a symlink")}
	}
	return full, nil
}

func WorkspacePath(workspaceRoot string, repo Repository, repositoryPath string) (string, bool) {
	clean, err := ValidateRelativePath(repositoryPath)
	if err != nil {
		return "", false
	}
	if repo.Relation == RelationNested || repo.Relation == RelationSymlink {
		return workspacePathThroughAlias(repo, clean)
	}
	abs := filepath.Join(repo.CanonicalRoot, filepath.FromSlash(clean))
	canonicalWorkspace, err := filepath.EvalSymlinks(workspaceRoot)
	if err == nil {
		rel, relErr := filepath.Rel(canonicalWorkspace, abs)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel), true
		}
	}
	// An ancestor/workspace repository can also be reachable through a
	// workspace symlink. Use that alias for files outside the direct workspace.
	return workspacePathThroughAlias(repo, clean)
}

func workspacePathThroughAlias(repo Repository, repositoryPath string) (string, bool) {
	aliases := make([]string, 0, len(repo.Aliases)+1)
	aliases = append(aliases, repo.DisplayPath)
	for _, alias := range repo.Aliases {
		if alias != repo.DisplayPath {
			aliases = append(aliases, alias)
		}
	}
	for _, alias := range aliases {
		if alias == "" || alias == "." || alias == ".." || strings.HasPrefix(alias, "../") {
			continue
		}
		prefix := repo.AliasPrefixes[alias]
		remainder := repositoryPath
		if prefix != "" {
			switch {
			case repositoryPath == prefix:
				remainder = ""
			case strings.HasPrefix(repositoryPath, prefix+"/"):
				remainder = strings.TrimPrefix(repositoryPath, prefix+"/")
			default:
				continue
			}
		}
		if remainder == "" {
			return alias, true
		}
		return filepath.ToSlash(filepath.Join(filepath.FromSlash(alias), filepath.FromSlash(remainder))), true
	}
	return "", false
}

type FileStatus struct {
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
	Status       string `json:"status"`
}

type Status struct {
	Staged    []FileStatus `json:"staged"`
	Unstaged  []FileStatus `json:"unstaged"`
	Untracked []FileStatus `json:"untracked"`
	Branch    string       `json:"branch"`
	Detached  bool         `json:"detached"`
	Ahead     int          `json:"ahead,omitempty"`
	Behind    int          `json:"behind,omitempty"`
}

type StatusSummary struct {
	Branch    string `json:"branch"`
	Detached  bool   `json:"detached"`
	Staged    int    `json:"staged"`
	Unstaged  int    `json:"unstaged"`
	Untracked int    `json:"untracked"`
	Total     int    `json:"total"`
	Ahead     int    `json:"ahead,omitempty"`
	Behind    int    `json:"behind,omitempty"`
}

func ReadStatus(ctx context.Context, root string) (*Status, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	result := &Status{Staged: []FileStatus{}, Unstaged: []FileStatus{}, Untracked: []FileStatus{}}
	records := bytes.Split(stdout.Bytes(), []byte{0})
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) == 0 {
			continue
		}
		if bytes.HasPrefix(record, []byte("# ")) {
			parseV2BranchHeader(result, string(record[2:]))
			continue
		}

		switch record[0] {
		case '?':
			if len(record) > 2 {
				path := string(record[2:])
				info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(path, "/"))))
				if statErr == nil && info.IsDir() {
					continue
				}
				result.Untracked = append(result.Untracked, FileStatus{Path: path, Status: "untracked"})
			}
		case '1':
			parts := bytes.SplitN(record, []byte{' '}, 9)
			if len(parts) == 9 {
				appendV2Status(result, parts[1], string(parts[8]), "")
			}
		case '2':
			parts := bytes.SplitN(record, []byte{' '}, 10)
			if len(parts) == 10 && i+1 < len(records) {
				appendV2Status(result, parts[1], string(parts[9]), string(records[i+1]))
				i++
			}
		case 'u':
			parts := bytes.SplitN(record, []byte{' '}, 11)
			if len(parts) == 11 {
				appendUnmergedStatus(result, string(parts[10]))
			}
		}
	}
	return result, nil
}

// Porcelain v2 uses several XY pairs for unmerged entries (DD, AU, UD, UA,
// DU, AA, and UU). They all represent an unresolved conflict; interpreting
// the two letters as ordinary index/worktree statuses would mislabel cases
// such as AA as "added". Keep the entry in both groups so callers can offer
// both index and worktree conflict actions while Summary still de-duplicates
// the path.
func appendUnmergedStatus(result *Status, path string) {
	file := FileStatus{Path: path, Status: "conflicted"}
	result.Staged = append(result.Staged, file)
	result.Unstaged = append(result.Unstaged, file)
}

func appendV2Status(result *Status, xy []byte, path, originalPath string) {
	if len(xy) != 2 {
		return
	}
	if xy[0] != '.' {
		result.Staged = append(result.Staged, FileStatus{Path: path, OriginalPath: originalPath, Status: statusName(xy[0])})
	}
	if xy[1] != '.' {
		result.Unstaged = append(result.Unstaged, FileStatus{Path: path, OriginalPath: originalPath, Status: statusName(xy[1])})
	}
}

func parseV2BranchHeader(status *Status, header string) {
	switch {
	case strings.HasPrefix(header, "branch.head "):
		branch := strings.TrimPrefix(header, "branch.head ")
		if branch == "(detached)" {
			status.Detached = true
			status.Branch = "HEAD"
		} else {
			status.Branch = branch
		}
	case strings.HasPrefix(header, "branch.ab "):
		fields := strings.Fields(strings.TrimPrefix(header, "branch.ab "))
		if len(fields) == 2 {
			status.Ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
			status.Behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
		}
	}
}

func (s *Status) Summary() StatusSummary {
	unique := make(map[string]bool)
	for _, list := range [][]FileStatus{s.Staged, s.Unstaged, s.Untracked} {
		for _, file := range list {
			unique[file.Path] = true
		}
	}
	return StatusSummary{
		Branch: s.Branch, Detached: s.Detached, Staged: len(s.Staged),
		Unstaged: len(s.Unstaged), Untracked: len(s.Untracked), Total: len(unique),
		Ahead: s.Ahead, Behind: s.Behind,
	}
}

func statusName(code byte) string {
	switch code {
	case 'M':
		return "modified"
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'U':
		return "conflicted"
	default:
		return "modified"
	}
}

func ReadRemoteURL(ctx context.Context, root string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// HasCommit reports whether HEAD resolves to a commit. An unborn repository
// can have a branch name and an origin, but it cannot be pushed or used as the
// source of a merge request yet.
func HasCommit(ctx context.Context, root string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "--quiet", "--end-of-options", "HEAD^{commit}")
	return cmd.Run() == nil
}
