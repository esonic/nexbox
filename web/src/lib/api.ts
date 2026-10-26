import type {
  Task, Project, Session, Asset, AssetVersion,
  ReviewComment, DiffResult, ChatMessage, MessagePart,
  TaskLabel, ModelInfo, AgentCapabilities,
} from "@/types";

const API_BASE = "/api";

// Paths that should NOT trigger an auth redirect on 401 — typically the
// auth endpoints themselves (the LoginPage needs to render the error
// inline rather than bouncing back to /login).
const AUTH_REDIRECT_SKIP = new Set<string>([
  "/login",
  "/logout",
  "/auth/me",
]);

function redirectToLogin() {
  if (typeof window === "undefined") return;
  const path = window.location.pathname;
  if (path === "/login" || path.startsWith("/login")) return;
  const from = encodeURIComponent(path + window.location.search);
  window.location.assign(`/login?from=${from}`);
}

export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly code?: string) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...options?.headers,
    },
  });
  if (!res.ok) {
    if (res.status === 401 && !AUTH_REDIRECT_SKIP.has(path)) {
      redirectToLogin();
    }
    const err = await res.json().catch(() => ({ error: res.statusText })) as { error?: string; code?: string };
    throw new ApiError(err.error || res.statusText, res.status, err.code);
  }
  const text = await res.text();
  if (!text) return undefined as T;
  return JSON.parse(text);
}

// ---------------------------------------------------------------------------
// Factory functions — create API objects for a given prefix ("" or "/connect")
// ---------------------------------------------------------------------------

function createTasksApi(prefix: string) {
  return {
    list: (limit = 30, offset = 0, projectId?: string, q?: string, source?: string) => {
      let url = `${prefix}/tasks?limit=${limit}&offset=${offset}`;
      if (projectId) url += `&project_id=${projectId}`;
      if (q) url += `&q=${encodeURIComponent(q)}`;
      if (source) url += `&source=${source}`;
      return request<Task[]>(url);
    },
    get: (id: string) =>
      request<Task>(`${prefix}/tasks/${id}`),
    create: (data: { title: string; description?: string; priority?: string; project_id?: string }) =>
      request<Task>(`${prefix}/tasks`, {
        method: "POST",
        body: JSON.stringify(data),
      }),
    update: (id: string, data: Partial<{ title: string; description: string; status: string; priority: string; project_id: string }>) =>
      request<Task>(`${prefix}/tasks/${id}`, {
        method: "PUT",
        body: JSON.stringify(data),
      }),
    delete: (id: string) =>
      request<void>(`${prefix}/tasks/${id}`, { method: "DELETE" }),
    addLabel: (taskId: string, labelName: string) =>
      request<void>(`${prefix}/tasks/${taskId}/labels`, {
        method: "POST",
        body: JSON.stringify({ name: labelName }),
      }),
    removeLabel: (taskId: string, labelName: string) =>
      request<void>(`${prefix}/tasks/${taskId}/labels/${encodeURIComponent(labelName)}`, { method: "DELETE" }),
  };
}

function createAssetsApi(prefix: string) {
  return {
    list: (projectId: string, limit = 30, offset = 0, type?: string, q?: string) => {
      let url = `${prefix}/projects/${projectId}/assets?limit=${limit}&offset=${offset}`;
      if (type) url += `&type=${type}`;
      if (q) url += `&q=${encodeURIComponent(q)}`;
      return request<Asset[]>(url);
    },
    get: (id: string) =>
      request<Asset & { content: string }>(`${prefix}/assets/${id}`),
    create: (projectId: string, data: { type: string; title: string; content: string }) =>
      request<Asset>(`${prefix}/projects/${projectId}/assets`, {
        method: "POST",
        body: JSON.stringify(data),
      }),
    update: (id: string, data: Partial<{ title: string; content: string; status: string }>) =>
      request<Asset>(`${prefix}/assets/${id}`, {
        method: "PUT",
        body: JSON.stringify(data),
      }),
    delete: (id: string) =>
      request<void>(`${prefix}/assets/${id}`, { method: "DELETE" }),
    versions: (id: string) =>
      request<AssetVersion[]>(`${prefix}/assets/${id}/versions`),
    diff: (id: string, v1: number, v2: number) =>
      request<DiffResult>(`${prefix}/assets/${id}/diff?v1=${v1}&v2=${v2}`),
  };
}

// Exported API type for type inference
export type TasksApiType = ReturnType<typeof createTasksApi>;
export type AssetsApiType = ReturnType<typeof createAssetsApi>;

// ---------------------------------------------------------------------------
// Data APIs
// ---------------------------------------------------------------------------

export const tasksApi = createTasksApi("");
export const assetsApi = createAssetsApi("");

// Projects
export const projectsApi = {
  list: () => request<Project[]>("/projects"),
  get: (id: string) => request<Project>(`/projects/${id}`),
  create: (data: {
    name: string;
    description?: string;
    auto_create_dir?: boolean;
    git_repo_path?: string;
  }) =>
    request<Project>("/projects", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (id: string, data: Partial<{ name: string; description: string }>) =>
    request<Project>(`/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (id: string) =>
    request<void>(`/projects/${id}`, { method: "DELETE" }),
  // Returns the per-project hidden "scratch" session, creating it on first
  // call. Used by the sidebar when the user clicks a project row to open a
  // new-chat surface without committing to a real session — the server
  // promotes it (is_hidden -> false) automatically as soon as the user sends
  // the first message.
  scratchSession: (id: string) =>
    request<Session>(`/projects/${id}/scratch-session`),
};

// Sessions
export const sessionsApi = {
  list: () =>
    request<{ sessions: Session[]; active_session_ids: string[] | null }>("/sessions"),
  listTrashed: () =>
    request<{ sessions: Session[] }>("/sessions/trash"),
  get: (id: string) =>
    request<Session>(`/sessions/${id}`),
  create: (data: { name?: string; project_id?: string; agent?: string; model?: string; effort?: string }) =>
    request<Session>("/sessions", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (id: string, data: Partial<{ name: string; project_id: string; agent: string; model: string; effort: string; chrome_browser_url: string; is_pinned: boolean }>) =>
    request<Session>(`/sessions/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (id: string) =>
    request<void>(`/sessions/${id}`, { method: "DELETE" }),
  restore: (id: string) =>
    request<Session>(`/sessions/${id}/restore`, { method: "POST" }),
  emptyTrash: () =>
    request<{ purged: number }>("/sessions/trash", { method: "DELETE" }),
  reorder: (sessionIds: string[]) =>
    request<void>("/sessions/reorder", {
      method: "PUT",
      body: JSON.stringify({ session_ids: sessionIds }),
    }),
  setWorktreeMode: (id: string, mode: "local" | "worktree") =>
    request<Session>(`/sessions/${id}/worktree-mode`, {
      method: "PUT",
      body: JSON.stringify({ mode }),
    }),
  getWorktreeStatus: (id: string) =>
    request<{ dirty: boolean; worktree_path?: string }>(`/sessions/${id}/worktree-status`),
  fork: (id: string, messageId: string, name?: string) =>
    request<Session>(`/sessions/${id}/fork`, {
      method: "POST",
      body: JSON.stringify({
        message_id: messageId,
        name,
      }),
    }),
  autoTitle: (id: string, message: string) =>
    request<Session>(`/sessions/${id}/auto-title`, {
      method: "POST",
      body: JSON.stringify({ message }),
    }),
};

// Chat Messages (per session)
export const messagesApi = {
  list: (sessionId: string, limit?: number, beforeId?: string) => {
    let url = `/sessions/${sessionId}/messages`;
    const params = new URLSearchParams();
    if (limit !== undefined) params.set('limit', limit.toString());
    if (beforeId) params.set('before', beforeId);
    if (params.toString()) url += `?${params.toString()}`;
    return request<{
      messages: ChatMessage[];
      active: boolean;
      has_more?: boolean;
      pending_input?: { toolUseId: string; toolName: string; input: string };
    }>(url);
  },
  getDetails: (messageId: string) =>
    request<{ parts: MessagePart[] }>(`/messages/${messageId}/details`),
};

// Message queue (per session)
export interface QueuedMessage {
  id: string;
  session_id: string;
  text: string;
  images?: { filename: string; url: string }[];
  created_at: string;
}

export const queueApi = {
  list: (sessionId: string) =>
    request<QueuedMessage[]>(`/sessions/${sessionId}/queue`),
  create: (sessionId: string, data: { text: string; images?: { filename: string; url: string }[] }) =>
    request<QueuedMessage>(`/sessions/${sessionId}/queue`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (sessionId: string, queueId: string, text: string) =>
    request<void>(`/sessions/${sessionId}/queue/${queueId}`, {
      method: "PUT",
      body: JSON.stringify({ text }),
    }),
  delete: (sessionId: string, queueId: string) =>
    request<void>(`/sessions/${sessionId}/queue/${queueId}`, { method: "DELETE" }),
  run: (sessionId: string, queueId: string) =>
    request<void>(`/sessions/${sessionId}/queue/${queueId}/run`, { method: "POST" }),
};

// Git operations (project-level)
export interface DiffFileInfo {
  path: string;
  original_path?: string;
  workspace_path?: string;
  status: string;
  insertions: number;
  deletions: number;
  binary?: boolean;
}

export interface DiffResponse {
  repository_id?: string;
  files: DiffFileInfo[];
  total_files: number;
  insertions: number;
  deletions: number;
  diff?: string;
  stats_included?: boolean;
  repository_count?: number;
}

export interface GitFileEntry {
  path: string;
  original_path?: string;
  status: string;
}

export interface GitStatusResponse {
  repository_id?: string;
  staged: GitFileEntry[];
  unstaged: GitFileEntry[];
  untracked: GitFileEntry[];
  ahead?: number;
}

export interface GitCommitInfo {
  hash: string;
  short_hash: string;
  message: string;
  author: string;
  date: string;
}

export interface GitCommitFileEntry {
  path: string;
  original_path?: string;
  workspace_path?: string;
  status: string;
}

export interface GitBranchInfo {
  name: string;
  is_current: boolean;
  is_remote: boolean;
  /** Branch name without the remote prefix; only set for remote-tracking branches. */
  local_name?: string;
  upstream?: string;
  ahead?: number;
  behind?: number;
}

export interface GitBranchResponse {
  repository_id?: string;
  current: string;
  branches: GitBranchInfo[];
}

export interface GitScope {
  projectId: string;
  sessionId?: string;
  repositoryId: string;
}

export interface GitRepository {
  id: string;
  name: string;
  display_path: string;
  relation: "workspace" | "ancestor" | "nested" | "symlink";
  is_symlink: boolean;
  shared: boolean;
  outside_workspace: boolean;
  aliases: string[];
  branch: string;
  detached: boolean;
  remote_name?: string;
  remote_url?: string;
  status: {
    branch: string;
    detached: boolean;
    staged: number;
    unstaged: number;
    untracked: number;
    total: number;
    ahead?: number;
    behind?: number;
  };
}

export interface GitRepositoriesResponse {
  workspace_mode: "local" | "worktree";
  default_repository_id: string;
  repositories: GitRepository[];
  dirty_repository_count: number;
  truncated: boolean;
  warnings: string[];
}

export interface GitWorkspaceStatusResponse {
  summary: {
    repository_count: number;
    dirty_repository_count: number;
    total_files: number;
    insertions: number;
    deletions: number;
    stats_included: boolean;
  };
  repositories: Array<{ repository_id: string; total_files: number; insertions: number; deletions: number }>;
  files: Array<{
    repository_id: string;
    repository_path: string;
    workspace_path: string;
    index_status?: string;
    worktree_status?: string;
  }>;
}

export interface RepositoryFileRef {
  repository_id: string;
  repository_name: string;
  repository_display_path: string;
  path: string;
  workspace_path?: string;
}

function gitQuery(scope: GitScope, extras?: Record<string, string | number | boolean | undefined>) {
  const params = new URLSearchParams();
  if (scope.sessionId) params.set("session_id", scope.sessionId);
  params.set("repository_id", scope.repositoryId);
  for (const [key, value] of Object.entries(extras ?? {})) {
    if (value !== undefined && value !== false && value !== "") params.set(key, String(value));
  }
  return `?${params.toString()}`;
}

function repositoryScoped<T extends { repository_id?: string }>(scope: GitScope, promise: Promise<T>): Promise<T> {
  return promise
    .then((result) => {
      if (result.repository_id !== scope.repositoryId) {
        throw new Error("Discarded a response from a previously selected Git repository");
      }
      return result;
    })
    .catch((error) => {
      reportInvalidRepositoryScope(scope, error);
      throw error;
    });
}

function repositoryRequest<T>(scope: GitScope, promise: Promise<T>): Promise<T> {
  return promise.catch((error) => {
    reportInvalidRepositoryScope(scope, error);
    throw error;
  });
}

function repositoryMutation(scope: GitScope, promise: Promise<void>): Promise<void> {
  return repositoryRequest(scope, promise);
}

function reportInvalidRepositoryScope(scope: GitScope, error: unknown) {
  if (!(error instanceof ApiError) || (error.code !== "repository_stale" && error.code !== "repository_not_found")) return;
  window.dispatchEvent(new CustomEvent("git-repository-invalid", { detail: scope }));
}

export const gitApi = {
  repositories: (projectId: string, sessionId?: string, refresh = false) => {
    const params = new URLSearchParams();
    if (sessionId) params.set("session_id", sessionId);
    if (refresh) params.set("refresh", "true");
    return request<GitRepositoriesResponse>(`/projects/${projectId}/git-repositories${params.toString() ? `?${params}` : ""}`);
  },
  workspaceStatus: (
    projectId: string,
    sessionId?: string,
    options: { includeFiles?: boolean; includeStats?: boolean } = {},
  ) => {
    const params = new URLSearchParams();
    if (sessionId) params.set("session_id", sessionId);
    if (options.includeFiles) params.set("include_files", "true");
    if (options.includeStats) params.set("include_stats", "true");
    return request<GitWorkspaceStatusResponse>(`/projects/${projectId}/git-workspace-status${params.toString() ? `?${params}` : ""}`);
  },
  status: (scope: GitScope) =>
    repositoryScoped(scope, request<GitStatusResponse>(`/projects/${scope.projectId}/git-status${gitQuery(scope)}`)),
  commit: (scope: GitScope, data: { file_paths: string[]; message: string }) =>
    repositoryScoped(scope, request<{ repository_id?: string; hash: string; short_hash: string }>(`/projects/${scope.projectId}/git-commit${gitQuery(scope)}`, {
      method: "POST",
      body: JSON.stringify(data),
    })),
  discard: (scope: GitScope, data: { file_path: string }) =>
    repositoryMutation(scope, request<void>(`/projects/${scope.projectId}/git-discard${gitQuery(scope)}`, {
      method: "POST",
      body: JSON.stringify(data),
    })),
  log: (scope: GitScope, limit?: number, skip?: number) =>
    repositoryScoped(scope, request<{ repository_id?: string; commits: GitCommitInfo[] }>(`/projects/${scope.projectId}/git-log${gitQuery(scope, { limit: limit || 20, skip })}`)).then((result) => result.commits),
  commitFiles: (scope: GitScope, hash: string) =>
    repositoryScoped(scope, request<{ repository_id?: string; files: GitCommitFileEntry[] }>(`/projects/${scope.projectId}/git-commit-files${gitQuery(scope, { hash })}`)).then((result) => result.files),
  commitDiff: (scope: GitScope, hash: string) =>
    repositoryScoped(scope, request<{ repository_id?: string; diff: string }>(`/projects/${scope.projectId}/git-commit-diff${gitQuery(scope, { hash })}`)),
  generateMessage: (scope: GitScope, data: { file_paths: string[] }) =>
    repositoryScoped(scope, request<{ repository_id?: string; message: string }>(`/projects/${scope.projectId}/generate-commit-message${gitQuery(scope)}`, {
      method: "POST",
      body: JSON.stringify(data),
    })),
  branches: (scope: GitScope) =>
    repositoryScoped(scope, request<GitBranchResponse>(`/projects/${scope.projectId}/git-branches${gitQuery(scope)}`)),
  checkout: (scope: GitScope, data: { branch: string; create?: boolean; start_point?: string }) =>
    repositoryScoped(scope, request<{ repository_id?: string; branch: string }>(`/projects/${scope.projectId}/git-checkout${gitQuery(scope)}`, {
      method: "POST",
      body: JSON.stringify(data),
    })),
  stage: (scope: GitScope, data: { file_paths: string[] }) =>
    repositoryMutation(scope, request<void>(`/projects/${scope.projectId}/git-stage${gitQuery(scope)}`, {
      method: "POST",
      body: JSON.stringify(data),
    })),
  unstage: (scope: GitScope, data: { file_paths: string[] }) =>
    repositoryMutation(scope, request<void>(`/projects/${scope.projectId}/git-unstage${gitQuery(scope)}`, {
      method: "POST",
      body: JSON.stringify(data),
    })),
  push: (scope: GitScope) =>
    repositoryScoped(scope, request<{ repository_id?: string; output: string }>(`/projects/${scope.projectId}/git-push${gitQuery(scope)}`, { method: "POST" })),
  pull: (scope: GitScope) =>
    repositoryScoped(scope, request<{ repository_id?: string; output: string }>(`/projects/${scope.projectId}/git-pull${gitQuery(scope)}`, { method: "POST" })),
};

// File browser
export interface FileEntry {
  name: string;
  path: string;
  is_dir: boolean;
  is_symlink?: boolean;
  size?: number;
  children?: FileEntry[];
}

export interface FileContentResponse {
  path: string;
  binary: boolean;
  size: number;
  content: string;
}

export interface FileSearchResult {
  path: string;
  name?: string;
  line?: number;
  content?: string;
}

export const filesApi = {
  // List immediate children of a directory. Pass empty path for the project root.
  list: (projectId: string, path: string, sessionId?: string) => {
    const params = new URLSearchParams();
    if (path) params.set("path", path);
    if (sessionId) params.set("session_id", sessionId);
    const qs = params.toString();
    return request<FileEntry[]>(`/projects/${projectId}/files${qs ? `?${qs}` : ""}`);
  },
  // Flat list of files (not directories) — used by the chat file picker.
  flat: (projectId: string, sessionId?: string) =>
    request<FileEntry[]>(`/projects/${projectId}/files-flat${sessionId ? `?session_id=${sessionId}` : ""}`),
  content: (projectId: string, path: string, sessionId?: string, repositoryId?: string) => {
    const promise = request<FileContentResponse>(`/projects/${projectId}/file-content?path=${encodeURIComponent(path)}${sessionId ? `&session_id=${sessionId}` : ""}${repositoryId ? `&repository_id=${encodeURIComponent(repositoryId)}` : ""}`);
    return repositoryId ? repositoryRequest({ projectId, sessionId, repositoryId }, promise) : promise;
  },
  // Committed (HEAD) version of a file, used as the dirty-diff baseline in the
  // editor. exists=false (with empty content) for new/untracked files.
  head: (projectId: string, path: string, sessionId?: string, repositoryId?: string) => {
    const promise = request<{ repository_id?: string; path: string; in_repo: boolean; exists: boolean; content: string }>(`/projects/${projectId}/git-file-head?path=${encodeURIComponent(path)}${sessionId ? `&session_id=${sessionId}` : ""}${repositoryId ? `&repository_id=${encodeURIComponent(repositoryId)}` : ""}`);
    return repositoryId
      ? repositoryScoped({ projectId, sessionId, repositoryId }, promise)
      : promise;
  },
  search: (projectId: string, query: string, type: "name" | "content" = "name", sessionId?: string, caseSensitive?: boolean) =>
    request<FileSearchResult[]>(`/projects/${projectId}/file-search?q=${encodeURIComponent(query)}&type=${type}${sessionId ? `&session_id=${sessionId}` : ""}${caseSensitive ? "&case=1" : ""}`),
  save: (projectId: string, path: string, content: string, sessionId?: string, repositoryId?: string) => {
    const promise = request<{ path: string; size?: number }>(
      `/projects/${projectId}/file-content?path=${encodeURIComponent(path)}${sessionId ? `&session_id=${sessionId}` : ""}${repositoryId ? `&repository_id=${encodeURIComponent(repositoryId)}` : ""}`,
      { method: "PUT", body: JSON.stringify({ content }) },
    );
    return repositoryId ? repositoryRequest({ projectId, sessionId, repositoryId }, promise) : promise;
  },
  delete: (projectId: string, path: string, sessionId?: string, repositoryId?: string) => {
    const promise = request<void>(
      `/projects/${projectId}/file-content?path=${encodeURIComponent(path)}${sessionId ? `&session_id=${sessionId}` : ""}${repositoryId ? `&repository_id=${encodeURIComponent(repositoryId)}` : ""}`,
      { method: "DELETE" },
    );
    return repositoryId ? repositoryRequest({ projectId, sessionId, repositoryId }, promise) : promise;
  },
  upload: async (
    projectId: string,
    directory: string,
    file: File,
    options?: { overwrite?: boolean; sessionId?: string },
  ): Promise<{ path: string; name: string; size: number } | { conflict: true; name: string; directory: string }> => {
    const params = new URLSearchParams();
    if (directory) params.set("path", directory);
    if (options?.overwrite) params.set("overwrite", "true");
    if (options?.sessionId) params.set("session_id", options.sessionId);
    const form = new FormData();
    form.append("file", file);
    const res = await fetch(`${API_BASE}/projects/${projectId}/file-upload${params.toString() ? `?${params}` : ""}`, {
      method: "POST",
      body: form,
    });
    if (res.status === 409) {
      const body = await res.json().catch(() => ({}));
      return { conflict: true, name: body.name ?? file.name, directory: body.directory ?? directory };
    }
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: res.statusText }));
      throw new Error(err.error || res.statusText);
    }
    return res.json();
  },
  downloadUrl: (projectId: string, path: string, sessionId?: string, repositoryId?: string) =>
    `${API_BASE}/projects/${projectId}/file-download?path=${encodeURIComponent(path)}${sessionId ? `&session_id=${sessionId}` : ""}${repositoryId ? `&repository_id=${encodeURIComponent(repositoryId)}` : ""}`,
  serveUrl: (projectId: string, path: string, sessionId?: string, repositoryId?: string) =>
    `${API_BASE}/projects/${projectId}/file-serve?path=${encodeURIComponent(path)}${sessionId ? `&session_id=${sessionId}` : ""}${repositoryId ? `&repository_id=${encodeURIComponent(repositoryId)}` : ""}`,
  move: (projectId: string, from: string, to: string, sessionId?: string) =>
    request<{ from: string; to: string }>(
      `/projects/${projectId}/file-move${sessionId ? `?session_id=${sessionId}` : ""}`,
      { method: "POST", body: JSON.stringify({ from, to }) },
    ),
  createWebShare: (projectId: string, path: string) =>
    request<{ id: string; url: string }>(
      `/projects/${projectId}/web-shares`,
      { method: "POST", body: JSON.stringify({ path }) },
    ),
};

export const diffApi = {
  get: (scope: GitScope) =>
    repositoryScoped(scope, request<DiffResponse>(`/projects/${scope.projectId}/diff${gitQuery(scope)}`)),
  stats: (scope: GitScope) =>
    repositoryScoped(scope, request<DiffResponse>(`/projects/${scope.projectId}/diff-stats${gitQuery(scope)}`)),
  fileDiff: (scope: GitScope, path: string, full?: boolean, staged?: boolean) =>
    repositoryScoped(scope, request<{ repository_id?: string; diff: string; too_large?: boolean; max_bytes?: number }>(`/projects/${scope.projectId}/file-diff${gitQuery(scope, { path, full, staged })}`)),
};

// Labels (project-scoped)
export const labelsApi = {
  list: (projectId: string) =>
    request<TaskLabel[]>(`/projects/${projectId}/labels`),
  create: (projectId: string, data: { name: string }) =>
    request<TaskLabel>(`/projects/${projectId}/labels`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  delete: (projectId: string, id: string) =>
    request<void>(`/projects/${projectId}/labels/${id}`, { method: "DELETE" }),
};

// Comments
export const commentsApi = {
  list: (assetId: string) =>
    request<ReviewComment[]>(`/assets/${assetId}/comments`),
  create: (assetId: string, data: { asset_version: number; line_number?: number; content: string; author?: string }) =>
    request<ReviewComment>(`/assets/${assetId}/comments`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (id: string, data: Partial<{ content: string; status: string }>) =>
    request<ReviewComment>(`/comments/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (id: string) =>
    request<void>(`/comments/${id}`, { method: "DELETE" }),
};

// Utility
export const utilApi = {
  validatePath: (path: string) =>
    request<{ valid: boolean; error?: string }>("/validate-path", {
      method: "POST",
      body: JSON.stringify({ path }),
    }),
  browseDir: (path?: string) =>
    request<{ current: string; parent: string; dirs: { name: string; path: string }[] }>(
      `/browse-dir${path ? `?path=${encodeURIComponent(path)}` : ""}`
    ),
};

// Changes (audit trail)
export interface ChangeRecord {
  id: number;
  namespace_id: string;
  project_id?: string;
  entity_type: string;
  entity_id: string;
  action: string;
  summary?: string;
  detail?: string;
  user_name: string;
  entity_version?: number;
  recipient?: string;
  task_id?: string;
  asset_id?: string;
  comment_id?: string;
  comment_preview?: string;
  line_number?: number;
  created_at: string;
}

export const changesApi = {
  list: (limit = 20, since = 0, order: "asc" | "desc" = "asc") =>
    request<ChangeRecord[]>(`/changes?limit=${limit}&since=${since}&order=${order}`),
};

// Image upload
export interface ImageUploadResponse {
  filename: string;
  url: string;
}

export const imageApi = {
  upload: async (sessionId: string, file: File): Promise<ImageUploadResponse> => {
    const form = new FormData();
    form.append("image", file);
    const res = await fetch(`${API_BASE}/sessions/${sessionId}/upload-image`, {
      method: "POST",
      body: form,
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: res.statusText }));
      throw new Error(err.error || res.statusText);
    }
    return res.json();
  },
};

// Server info
export interface ServerInfo {
  agent: string;
  mode: "local" | "cloud" | "needs_login";
  version?: string;
  namespace?: string;
  user_name?: string;
  server_url?: string;
}

export interface PromptTemplate {
  name: string;
  content: string;
}

export const promptTemplatesApi = {
  list: () => request<PromptTemplate[]>("/prompt-templates"),
  replace: (templates: PromptTemplate[]) =>
    request<PromptTemplate[]>("/prompt-templates", {
      method: "PUT",
      body: JSON.stringify(templates),
    }),
};

export const serverApi = {
  info: () => request<ServerInfo>("/info"),
  availableAgents: () => request<{
    agents: string[];
    default: string;
    capabilities: Record<string, AgentCapabilities>;
  }>("/available-agents"),
  availableModels: (agent: string) => request<{ agent: string; models: string[]; models_info?: ModelInfo[]; source: string }>(`/available-models?agent=${encodeURIComponent(agent)}`),
  setDefaultAgent: (agent: string) =>
    request<{ default: string }>("/default-agent", {
      method: "PUT",
      body: JSON.stringify({ agent }),
    }),
  getSettings: () =>
    request<{ default_agent: string; chrome_headless: boolean; suggest_next_actions: boolean; mcp_tools?: { task_management?: boolean } }>("/settings"),
  updateSettings: (data: { default_agent?: string; chrome_headless?: boolean; suggest_next_actions?: boolean; mcp_tools?: { task_management?: boolean } }) =>
    request<void>("/settings", {
      method: "PUT",
      body: JSON.stringify(data),
    }),
};

// Server info extended for cloud mode
export interface CloudInfo extends ServerInfo {
  mode: "local" | "cloud";
  server_url?: string;
  namespace?: string;
  user_name?: string;
}

export type AuthStatus =
  | { auth_required: false }
  | { auth_required: true; username: string; user_id: number };

// Password authentication is optional; account-free instances return local mode.
export const authApi = {
  login: (username: string, password: string) =>
    request<AuthStatus>("/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  logout: () =>
    request<void>("/logout", { method: "POST" }),
  me: () =>
    request<AuthStatus>("/auth/me"),
};

// ---------------------------------------------------------------------------
// Marketplace
// ---------------------------------------------------------------------------


// Local plugin (from marketplace directory — supports JSON, SKILL.md, and .claude-plugin formats)
export interface LocalPlugin {
  id: string;
  name: string;
  description: string;
  type: string;       // "mcp", "skill", "plugin", "lsp"
  version?: string;
  command?: string;
  args?: string[];
  env_vars?: string[];
  author?: string;
  source?: string;       // "custom", "skill", "plugin", "connector"
  mcp_config?: string;   // raw .mcp.json content
  lsp_config?: string;   // raw .lsp.json content
  auth_type?: string;    // "oauth", "apikey", "oauth_discovery", "none"
  install_path?: string; // absolute path on disk
  category?: string;
  homepage?: string;
  components?: string[];
  source_label?: string; // short display label, e.g. "anthropics/skills"
  source_full?: string;  // full path or git URL for tooltip
}

export interface InstalledMCP {
  name: string;
  project_id: string;   // "" = global, else project-scoped
  package_id: string;
  version: string;
  mcp_key: string;
  manifest: string;
  env_values: string;   // JSON: user-provided env secrets
  auth_type: string;    // "none", "apikey", "oauth_discovery", "oauth"
  installed_at: number;
}

export interface InstalledSkill {
  name: string;
  project_id: string;
  version: string;
  install_path: string;
  installed_at: number;
  missing: boolean;
}

export interface InstalledPluginEntry {
  name: string;
  project_id: string;
  install_path: string;
  installed_at: number;
}

export interface MarketplaceSource {
  id: string;
  type: string;   // "local", "git", or "mcp"
  url: string;
  path: string;
  name: string;
  added_at: number;
}

export const marketplaceApi = {
  // Local plugins (directory-based)
  listPlugins: () => request<LocalPlugin[]>("/marketplace/plugins"),

  getPlugin: (id: string) => request<LocalPlugin>(`/marketplace/plugins/${id}`),

  // Marketplace sources (Add Marketplace)
  listSources: () => request<MarketplaceSource[]>("/marketplace/sources"),

  addSource: (input: string) =>
    request<MarketplaceSource>("/marketplace/sources", {
      method: "POST",
      body: JSON.stringify({ input }),
    }),

  addMCPSource: (name: string, url: string, headers: Record<string, string>) =>
    request<MarketplaceSource>("/marketplace/mcp-sources", {
      method: "POST",
      body: JSON.stringify({ name, url, headers }),
    }),

  removeSource: (id: string) =>
    request<void>(`/marketplace/sources/${encodeURIComponent(id)}`, { method: "DELETE" }),

  // Installed MCPs
  listInstalledMCPs: (projectId?: string) => {
    const qs = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<InstalledMCP[]>(`/marketplace/installed-mcps${qs}`);
  },

  installMCP: (body: { name: string; package_id?: string; version?: string; mcp_key?: string; manifest: object; env?: Record<string, string>; project_id?: string; auth_type?: string }) =>
    request<InstalledMCP>("/marketplace/installed-mcps", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  uninstallMCP: (name: string, projectId?: string) => {
    const qs = projectId !== undefined ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<void>(`/marketplace/installed-mcps/${encodeURIComponent(name)}${qs}`, {
      method: "DELETE",
    });
  },

  // Installed Skills
  listInstalledSkills: (projectId?: string) => {
    const qs = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<InstalledSkill[]>(`/marketplace/installed-skills${qs}`);
  },

  installSkill: (body: { name: string; package_id: string; version: string; project_id?: string; install_path?: string }) =>
    request<InstalledSkill>("/marketplace/installed-skills", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  uninstallSkill: (name: string, projectId?: string) => {
    const qs = projectId !== undefined ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<void>(
      `/marketplace/installed-skills/${encodeURIComponent(name)}${qs}`,
      { method: "DELETE" }
    );
  },

  // Installed Plugins (--plugin-dir)
  listInstalledPlugins: (projectId?: string) => {
    const qs = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<InstalledPluginEntry[]>(`/marketplace/installed-plugins${qs}`);
  },

  installPlugin: (body: { name: string; install_path?: string; lsp_config?: string; project_id?: string }) =>
    request<InstalledPluginEntry>("/marketplace/installed-plugins", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  uninstallPlugin: (name: string, projectId?: string) => {
    const qs = projectId !== undefined ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<void>(
      `/marketplace/installed-plugins/${encodeURIComponent(name)}${qs}`,
      { method: "DELETE" }
    );
  },

  // MCP OAuth
  oauthStart: (name: string, projectId?: string) => {
    const qs = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<{ auth_url: string }>(`/connectors/${encodeURIComponent(name)}/oauth/start${qs}`, {
      method: "POST",
    });
  },

  // Probe an installed MCP to detect whether OAuth is required.
  // If the upstream returns 401, the server broadcasts connector:needs_auth via
  // WebSocket so the frontend's existing "Authorize" toast is shown automatically.
  probeMCP: (name: string, projectId?: string) => {
    const qs = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<{ name: string; key: string; state: "ready" | "needs_auth" | "auth_rejected" | "unavailable" | "runtime_managed"; tool_count?: number; message?: string }>(`/connectors/${encodeURIComponent(name)}/probe${qs}`, { method: "POST" });
  },
};
