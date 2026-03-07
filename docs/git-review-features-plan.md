# Complete Git Review Features

## Current State

Backend has `GET /iterations/:id/diff` (full diff) and `GET /iterations/:id/diff-stats` (stats only). Frontend has `DiffView.tsx` with file list sidebar, filter, mark-as-viewed. No git commit, git log, git add, or git status APIs exist yet.

## 1. Backend: New Git APIs in `server/api/git.go`

Add a new file `server/api/git.go` with these handlers:

- **`GetGitStatus`** (`GET /iterations/:id/git-status`)
  - Runs `git status --porcelain=v1` to get staged/unstaged/untracked files
  - Returns `{ staged: [], unstaged: [], untracked: [] }` with per-file status (M/A/D/R)
  - Used by frontend to show file categories
- **`GitCommit`** (`POST /iterations/:id/git-commit`)
  - Input: `{ file_paths: string[], message: string }`
  - Implements atomic commit pattern from 1code: `git reset HEAD` -> `git add -- <files>` -> `git commit -m <msg>`
  - Returns `{ hash: string, short_hash: string }`
- **`GitDiscard`** (`POST /iterations/:id/git-discard`)
  - Input: `{ file_path: string }`
  - For tracked files: `git checkout HEAD -- <file>`
  - For untracked/added: `os.Remove(filepath.Join(workDir, file))`
- **`GitLog`** (`GET /iterations/:id/git-log?limit=50`)
  - Runs `git log -N --format=%H|%h|%s|%an|%aI`
  - Returns `[]{ hash, short_hash, message, author, date }`
- **`GitCommitFiles`** (`GET /iterations/:id/git-commit-files?hash=xxx`)
  - Runs `git diff-tree --no-commit-id --name-status -r <hash>`
  - Returns changed files for a specific commit
- **`GitCommitDiff`** (`GET /iterations/:id/git-commit-diff?hash=xxx`)
  - Runs `git diff <hash>^ <hash>` (or `git diff --root <hash>` for first commit)
  - Returns full unified diff for a specific commit
- **`GenerateCommitMessage`** (`POST /iterations/:id/generate-commit-message`)
  - Input: `{ file_paths: string[] }`
  - Heuristic-based (like 1code fallback): detect type from file paths (feat/fix/docs/test/chore), generate conventional commit message
  - No LLM call needed for now; can be enhanced later

Register all routes in `server/api/router.go` under the `/iterations/{iterationId}` group.

## 2. Frontend: API Client Updates in `web/src/lib/api.ts`

Add to `web/src/lib/api.ts`:

```typescript
export const gitApi = {
  status: (iterationId: string) => request<GitStatusResponse>(`/iterations/${iterationId}/git-status`),
  commit: (iterationId: string, data: { file_paths: string[]; message: string }) =>
    request<{ hash: string; short_hash: string }>(`/iterations/${iterationId}/git-commit`, { method: "POST", body: JSON.stringify(data) }),
  discard: (iterationId: string, data: { file_path: string }) =>
    request<void>(`/iterations/${iterationId}/git-discard`, { method: "POST", body: JSON.stringify(data) }),
  log: (iterationId: string, limit?: number) =>
    request<GitCommitInfo[]>(`/iterations/${iterationId}/git-log?limit=${limit || 50}`),
  commitFiles: (iterationId: string, hash: string) =>
    request<GitCommitFile[]>(`/iterations/${iterationId}/git-commit-files?hash=${hash}`),
  commitDiff: (iterationId: string, hash: string) =>
    request<{ diff: string }>(`/iterations/${iterationId}/git-commit-diff?hash=${hash}`),
  generateMessage: (iterationId: string, data: { file_paths: string[] }) =>
    request<{ message: string }>(`/iterations/${iterationId}/generate-commit-message`, { method: "POST", body: JSON.stringify(data) }),
};
```

## 3. Enhanced DiffView with Tabs: Changes + History

Restructure `web/src/components/diff/DiffView.tsx`:

**Props change**: Accept `iterationId` alongside `diffText` to enable commit/discard/history features.

**Changes Tab** (current view, enhanced):

- File list sidebar: add **checkboxes** next to each file for selective commit
- Add **discard button** (trash icon, hover-visible) per file in the sidebar - calls `gitApi.discard`
- Add **commit input area** at the bottom of the file list sidebar:
  - Text input for commit message
  - "AI" button to auto-generate message via `gitApi.generateMessage`
  - "Commit" button - commits only checked files via `gitApi.commit`
  - Show count: "X of Y files selected"

**History Tab** (new):

- List of commits from `gitApi.log` showing: short hash, message, author, relative date
- Click a commit to load its diff via `gitApi.commitDiff` and show in the diff content area
- Show changed files for selected commit via `gitApi.commitFiles`

## 4. Review Bar Enhancement

In `web/src/components/chat/ChatPanel.tsx`:

- Pass `iterationId` to `DiffView` so it can call git APIs
- After successful commit, refresh diff stats

## Implementation Order

1. Backend git APIs (git.go + router)
2. Frontend API client
3. DiffView: checkboxes + discard + commit input
4. DiffView: History tab
5. Wire everything together in ChatPanel
