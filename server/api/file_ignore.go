package api

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// gitIgnoreFilter answers "is this path hidden by the repository's ignore rules?"
// using git itself, so file search agrees with VSCode — whose quick open and
// full-text search both default to search.useIgnoreFiles: true. Without it a
// Maven project's target/ (or a Rust target/, a Go bin/, any project-specific
// ignore) floods the results with build output.
type gitIgnoreFilter struct {
	files map[string]bool // ignored files, path relative to the root, slash-separated
	dirs  map[string]bool // directories whose entire subtree is ignored
}

// ignored reports whether rel (relative to the filter's root) is excluded. A nil
// filter excludes nothing, so callers can treat "not a git repository" as "no
// ignore rules" without a branch.
func (f *gitIgnoreFilter) ignored(rel string, isDir bool) bool {
	if f == nil || rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	rel = filepath.ToSlash(rel)
	if isDir {
		return f.dirs[rel]
	}
	return f.files[rel]
}

const gitIgnoreCacheTTL = 5 * time.Second

type gitIgnoreCacheEntry struct {
	filter  *gitIgnoreFilter
	fetched time.Time
}

var (
	gitIgnoreCacheMu sync.Mutex
	gitIgnoreCache   = map[string]gitIgnoreCacheEntry{}
)

// ignoreFilterForRoot returns the ignore rules in effect for root, or nil when
// root is not inside a git repository (callers then fall back to the skipDir
// deny-list alone).
//
// The result is cached briefly because interactive search re-lists on every
// keystroke. Caching is safe here precisely because the filter enumerates only
// *ignored* paths: a stale entry can never hide a newly created file, it can
// only delay a just-edited .gitignore by a few seconds.
func ignoreFilterForRoot(root string) *gitIgnoreFilter {
	gitIgnoreCacheMu.Lock()
	entry, ok := gitIgnoreCache[root]
	gitIgnoreCacheMu.Unlock()
	if ok && time.Since(entry.fetched) < gitIgnoreCacheTTL {
		return entry.filter
	}

	// Loaded outside the lock: a concurrent duplicate git call is cheaper than
	// serialising every search behind one mutex.
	filter := loadGitIgnoreFilter(root)

	gitIgnoreCacheMu.Lock()
	gitIgnoreCache[root] = gitIgnoreCacheEntry{filter: filter, fetched: time.Now()}
	gitIgnoreCacheMu.Unlock()
	return filter
}

func loadGitIgnoreFilter(root string) *gitIgnoreFilter {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// --others --ignored lists ignored paths git does not track, and --directory
	// collapses a fully-ignored directory into a single "target/" entry instead of
	// descending into it, which keeps this one call cheap. --exclude-standard
	// applies the same sources git (and ripgrep, and therefore VSCode) uses:
	// .gitignore at every level, .git/info/exclude, and core.excludesFile.
	//
	// Tracked files never appear in the output, so a checked-in file stays
	// searchable even when a later ignore rule would match it — same as git status.
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z",
		"--others", "--ignored", "--directory", "--exclude-standard")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil // not a repository, or git unavailable
	}

	filter := &gitIgnoreFilter{files: map[string]bool{}, dirs: map[string]bool{}}
	for _, entry := range strings.Split(stdout.String(), "\x00") {
		if entry == "" {
			continue
		}
		if strings.HasSuffix(entry, "/") {
			filter.dirs[strings.TrimSuffix(entry, "/")] = true
			continue
		}
		filter.files[entry] = true
	}
	return filter
}

// excludedFromSearch reports whether a walked entry should be kept out of search
// results. skipDir stays in force as an unconditional deny-list (it covers
// node_modules and .git the way VSCode's default files.exclude does, and repos
// that fail to ignore their own build output); gitignore is layered on top.
func excludedFromSearch(filter *gitIgnoreFilter, root, path string, isDir bool) bool {
	if isDir && skipDir(filepath.Base(path)) {
		return true
	}
	if filter == nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return filter.ignored(rel, isDir)
}
