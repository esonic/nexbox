import type {
  Task, Project, Session, Asset, AssetVersion,
  ReviewComment, DiffResult, ChatMessage, MessagePart,
  TaskAsset, TaskLabel, TaskComment, ReactionSummary,
} from "@/types";

const API_BASE = "/api";

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...options?.headers,
    },
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(err.error || res.statusText);
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
    list: (limit = 30, offset = 0, projectId?: string, q?: string) => {
      let url = `${prefix}/tasks?limit=${limit}&offset=${offset}`;
      if (projectId) url += `&project_id=${projectId}`;
      if (q) url += `&q=${encodeURIComponent(q)}`;
      return request<Task[]>(url);
    },
    get: (id: string) =>
      request<Task>(`${prefix}/tasks/${id}`),
    create: (data: { title: string; parent_id?: string; description?: string; priority?: string; project_id?: string; assigner?: string }) =>
      request<Task>(`${prefix}/tasks`, {
        method: "POST",
        body: JSON.stringify(data),
      }),
    update: (id: string, data: Partial<{ title: string; description: string; status: string; priority: string; parent_id: string; project_id: string; assigner: string }>) =>
      request<Task>(`${prefix}/tasks/${id}`, {
        method: "PUT",
        body: JSON.stringify(data),
      }),
    delete: (id: string) =>
      request<void>(`${prefix}/tasks/${id}`, { method: "DELETE" }),
    incrementRefCount: (taskId: string) =>
      request<void>(`${prefix}/tasks/${taskId}/ref-count`, { method: "POST" }),
    // Task-Asset associations
    listAssets: (taskId: string) =>
      request<Asset[]>(`${prefix}/tasks/${taskId}/assets`),
    linkAsset: (taskId: string, assetId: string) =>
      request<TaskAsset>(`${prefix}/tasks/${taskId}/assets`, {
        method: "POST",
        body: JSON.stringify({ asset_id: assetId }),
      }),
    unlinkAsset: (taskId: string, assetId: string) =>
      request<void>(`${prefix}/tasks/${taskId}/assets/${assetId}`, { method: "DELETE" }),
    // Task comments
    listComments: (taskId: string) =>
      request<TaskComment[]>(`${prefix}/tasks/${taskId}/comments`),
    createComment: (taskId: string, data: { body: string }) =>
      request<TaskComment>(`${prefix}/tasks/${taskId}/comments`, {
        method: "POST",
        body: JSON.stringify(data),
      }),
    updateComment: (commentId: string, data: { body: string }) =>
      request<TaskComment>(`${prefix}/task-comments/${commentId}`, {
        method: "PUT",
        body: JSON.stringify(data),
      }),
    deleteComment: (commentId: string) =>
      request<void>(`${prefix}/task-comments/${commentId}`, { method: "DELETE" }),
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
  create: (data: { namespace_id?: string; name: string; description?: string; git_repo_path?: string }) =>
    request<Project>("/projects", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (id: string, data: Partial<{
    name: string; description: string; git_repo_path: string;
    okone: Project["okone"];
  }>) =>
    request<Project>(`/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (id: string) =>
    request<void>(`/projects/${id}`, { method: "DELETE" }),
  listMembers: (id: string) =>
    request<{ user_name: string; created_at: string }[]>(`/projects/${id}/members`),
  addMember: (id: string, userName: string) =>
    request<void>(`/projects/${id}/members`, {
      method: "POST",
      body: JSON.stringify({ user_name: userName }),
    }),
  removeMember: (id: string, userName: string) =>
    request<void>(`/projects/${id}/members/${encodeURIComponent(userName)}`, { method: "DELETE" }),
  leave: (id: string) =>
    request<void>(`/projects/${id}/members/me`, { method: "DELETE" }),
  lookupByDir: (dir: string) =>
    request<{ remote_id: string; git_repo_path: string }>(`/projects/lookup-by-dir?dir=${encodeURIComponent(dir)}`),
  checkGitDir: (path: string) =>
    request<Project>(`/projects/by-git-dir?path=${encodeURIComponent(path)}`),
  addToSidebar: (id: string, localDir: string) =>
    request<Project>(`/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify({ git_repo_path: localDir }),
    }),
  removeFromSidebar: (id: string) =>
    request<Project>(`/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify({ git_repo_path: "" }),
    }),
};

// Sessions
export const sessionsApi = {
  list: () =>
    request<{ sessions: Session[]; active_session_ids: string[] | null }>("/sessions"),
  listTrashed: () =>
    request<{ sessions: Session[] }>("/sessions/trash"),
  get: (id: string) =>
    request<Session>(`/sessions/${id}`),
  create: (data: { name?: string; project_id?: string; agent?: string }) =>
    request<Session>("/sessions", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (id: string, data: Partial<{ name: string; project_id: string; agent: string }>) =>
    request<Session>(`/sessions/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (id: string) =>
    request<void>(`/sessions/${id}`, { method: "DELETE" }),
  restore: (id: string) =>
    request<Session>(`/sessions/${id}/restore`, { method: "POST" }),
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
};

// Chat Messages (per session)
export const messagesApi = {
  list: (sessionId: string, limit?: number, beforeId?: string) => {
    let url = `/sessions/${sessionId}/messages`;
    const params = new URLSearchParams();
    if (limit !== undefined) params.set('limit', limit.toString());
    if (beforeId) params.set('before', beforeId);
    if (params.toString()) url += `?${params.toString()}`;
    return request<{ messages: ChatMessage[]; active: boolean; has_more?: boolean }>(url);
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
  status: string;
  insertions: number;
  deletions: number;
}

export interface DiffResponse {
  files: DiffFileInfo[];
  total_files: number;
  insertions: number;
  deletions: number;
  diff?: string;
}

export interface GitFileEntry {
  path: string;
  status: string;
}

export interface GitStatusResponse {
  staged: GitFileEntry[];
  unstaged: GitFileEntry[];
  untracked: GitFileEntry[];
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
  status: string;
}

export interface GitBranchInfo {
  name: string;
  is_current: boolean;
  is_remote: boolean;
  upstream?: string;
  ahead?: number;
  behind?: number;
}

export interface GitBranchResponse {
  current: string;
  branches: GitBranchInfo[];
}

function sid(sessionId?: string) {
  return sessionId ? `?session_id=${sessionId}` : "";
}
function sidAmp(sessionId?: string) {
  return sessionId ? `&session_id=${sessionId}` : "";
}

export const gitApi = {
  status: (projectId: string, sessionId?: string) =>
    request<GitStatusResponse>(`/projects/${projectId}/git-status${sid(sessionId)}`),
  commit: (projectId: string, data: { file_paths: string[]; message: string }, sessionId?: string) =>
    request<{ hash: string; short_hash: string }>(`/projects/${projectId}/git-commit${sid(sessionId)}`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  discard: (projectId: string, data: { file_path: string }, sessionId?: string) =>
    request<void>(`/projects/${projectId}/git-discard${sid(sessionId)}`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  log: (projectId: string, limit?: number, skip?: number, sessionId?: string) =>
    request<GitCommitInfo[]>(`/projects/${projectId}/git-log?limit=${limit || 20}${skip ? `&skip=${skip}` : ""}${sidAmp(sessionId)}`),
  commitFiles: (projectId: string, hash: string, sessionId?: string) =>
    request<GitCommitFileEntry[]>(`/projects/${projectId}/git-commit-files?hash=${hash}${sidAmp(sessionId)}`),
  commitDiff: (projectId: string, hash: string, sessionId?: string) =>
    request<{ diff: string }>(`/projects/${projectId}/git-commit-diff?hash=${hash}${sidAmp(sessionId)}`),
  generateMessage: (projectId: string, data: { file_paths: string[] }, sessionId?: string) =>
    request<{ message: string }>(`/projects/${projectId}/generate-commit-message${sid(sessionId)}`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  branches: (projectId: string, sessionId?: string) =>
    request<GitBranchResponse>(`/projects/${projectId}/git-branches${sid(sessionId)}`),
  checkout: (projectId: string, data: { branch: string; create?: boolean }, sessionId?: string) =>
    request<{ branch: string }>(`/projects/${projectId}/git-checkout${sid(sessionId)}`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  stage: (projectId: string, data: { file_paths: string[] }, sessionId?: string) =>
    request<void>(`/projects/${projectId}/git-stage${sid(sessionId)}`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  unstage: (projectId: string, data: { file_paths: string[] }, sessionId?: string) =>
    request<void>(`/projects/${projectId}/git-unstage${sid(sessionId)}`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  push: (projectId: string, sessionId?: string) =>
    request<{ output: string }>(`/projects/${projectId}/git-push${sid(sessionId)}`, { method: "POST" }),
  pull: (projectId: string, sessionId?: string) =>
    request<{ output: string }>(`/projects/${projectId}/git-pull${sid(sessionId)}`, { method: "POST" }),
};

// File browser
export interface FileEntry {
  name: string;
  path: string;
  is_dir: boolean;
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
  tree: (projectId: string, sessionId?: string) =>
    request<FileEntry[]>(`/projects/${projectId}/files${sessionId ? `?session_id=${sessionId}` : ""}`),
  content: (projectId: string, path: string, sessionId?: string) =>
    request<FileContentResponse>(`/projects/${projectId}/file-content?path=${encodeURIComponent(path)}${sessionId ? `&session_id=${sessionId}` : ""}`),
  search: (projectId: string, query: string, type: "name" | "content" = "name", sessionId?: string) =>
    request<FileSearchResult[]>(`/projects/${projectId}/file-search?q=${encodeURIComponent(query)}&type=${type}${sessionId ? `&session_id=${sessionId}` : ""}`),
};

export const diffApi = {
  get: (projectId: string, sessionId?: string) =>
    request<DiffResponse>(`/projects/${projectId}/diff${sid(sessionId)}`),
  stats: (projectId: string, sessionId?: string) =>
    request<DiffResponse>(`/projects/${projectId}/diff-stats${sid(sessionId)}`),
  fileDiff: (projectId: string, path: string, full?: boolean, sessionId?: string) =>
    request<{ diff: string }>(`/projects/${projectId}/file-diff?path=${encodeURIComponent(path)}${full ? "&full=true" : ""}${sidAmp(sessionId)}`),
};

// GitLab integration
export interface CreateMRResponse {
  iid: number;
  web_url: string;
  title: string;
  state: string;
}

export const gitlabApi = {
  createMR: (projectId: string) =>
    request<CreateMRResponse>(`/projects/${projectId}/create-mr`, { method: "POST" }),
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

// Reactions (generic)
export const reactionsApi = {
  get: (targetType: string, targetId: string) =>
    request<ReactionSummary[]>(`/reactions/${targetType}/${targetId}`),
  add: (targetType: string, targetId: string, emoji: string) =>
    request<void>(`/reactions/${targetType}/${targetId}`, {
      method: "POST",
      body: JSON.stringify({ emoji }),
    }),
  remove: (targetType: string, targetId: string, emoji: string) =>
    request<void>(`/reactions/${targetType}/${targetId}?emoji=${encodeURIComponent(emoji)}`, {
      method: "DELETE",
    }),
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

export const serverApi = {
  info: () => request<ServerInfo>("/info"),
  availableAgents: () => request<{ agents: string[]; default: string }>("/available-agents"),
  setDefaultAgent: (agent: string) =>
    request<{ default: string }>("/default-agent", {
      method: "PUT",
      body: JSON.stringify({ agent }),
    }),
  getSettings: () =>
    request<{ default_agent: string; has_token: boolean; masked_token: string; has_gitlab_token: boolean; masked_gitlab_token: string; disable_cloud: boolean }>("/settings"),
  updateSettings: (data: { default_agent?: string; token?: string; gitlab_token?: string; disable_cloud?: boolean }) =>
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

export const updateApi = {
  status: () => request<{ state: string; remote_version?: string }>("/update/status"),
  restart: () => request<{ restarting: boolean }>("/update/restart", { method: "POST" }),
};

export const authApi = {
  loginUrl: () => request<{ url: string }>("/auth/login-url"),
  logout: () => request<{ ok: boolean }>("/auth/logout", { method: "POST" }),
};

// Members (namespace-level fuzzy search)
export interface Member {
  user_name: string;
  display_name: string;
  avatar_url: string;
}

export const membersApi = {
  search: (q: string) =>
    request<Member[]>(`/members?q=${encodeURIComponent(q)}`),
};

// ---------------------------------------------------------------------------
// Marketplace
// ---------------------------------------------------------------------------

// Predefined package categories (分类). Values are canonical tag strings stored in `tags[0]`.
// Kept here so both the publish form and the filter bar share the same list.
export const PACKAGE_CATEGORIES = [
  { value: "dev",           label: "Dev",           description: "Code, CI/CD, testing" },
  { value: "data",          label: "Data",          description: "Databases, analytics, ETL" },
  { value: "ops",           label: "Ops",           description: "Monitoring, deployment, infrastructure" },
  { value: "productivity",  label: "Productivity",  description: "Automation, scheduling, task management" },
  { value: "communication", label: "Communication", description: "IM, email, notifications" },
  { value: "security",      label: "Security",      description: "Auth, scanning, compliance" },
  { value: "ai",            label: "AI",            description: "Models, embeddings, knowledge bases" },
] as const;

export type PackageCategoryValue = typeof PACKAGE_CATEGORIES[number]["value"];

export interface MarketplacePackage {
  id: string;
  name: string;
  type: "mcp" | "skill";
  title: string;
  description: string;
  author_id: string;
  latest_version: string;
  tags: string[]; // currently at most one element (the category value)
  install_count: number;
  star_count: number;
  created_at: string;
  updated_at: string;
}

export interface MarketplacePackageVersion {
  id: string;
  package_id: string;
  version: string;
  manifest: string; // JSON string
  oss_key: string;
  changelog: string;
  published_by: string;
  created_at: string;
}

export interface MarketplacePackageDetail {
  package: MarketplacePackage;
  versions: MarketplacePackageVersion[];
}

export interface PackageStarStatus {
  starred: boolean;
  star_count: number;
}

export interface PackageReview {
  id: string;
  package_id: string;
  user_name: string;
  body: string;
  created_at: string;
  updated_at: string;
}

export interface InstalledMCP {
  name: string;
  project_id: string;   // "" = global, else project-scoped
  package_id: string;   // remote marketplace package ID (for update checks)
  version: string;      // installed version string
  mcp_key: string;
  manifest: string;
  env_values: string;   // JSON: user-provided env secrets
  installed_at: number;
}

export interface InstalledSkill {
  name: string;
  project_id: string;
  version: string;
  install_path: string;
  installed_at: number;
  missing: boolean;     // true if install_path no longer exists on disk
}

export const marketplaceApi = {
  listPackages: (params?: { type?: string; q?: string; tag?: string; sort?: string; limit?: number; offset?: number }) => {
    const qs = new URLSearchParams();
    if (params?.type) qs.set("type", params.type);
    if (params?.q) qs.set("q", params.q);
    if (params?.tag) qs.set("tag", params.tag);
    if (params?.sort) qs.set("sort", params.sort);
    if (params?.limit != null) qs.set("limit", String(params.limit));
    if (params?.offset != null) qs.set("offset", String(params.offset));
    const s = qs.toString();
    return request<MarketplacePackage[]>(`/marketplace/packages${s ? "?" + s : ""}`);
  },

  getPackage: (id: string) =>
    request<MarketplacePackageDetail>(`/marketplace/packages/${id}`),

  publishPackage: (body: {
    name: string;
    type: "mcp" | "skill";
    title: string;
    description: string;
    version: string;
    manifest: object;
    changelog?: string;
    tags?: string[];
  }) =>
    request<MarketplacePackageDetail>("/marketplace/packages", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  updatePackage: (id: string, body: { title?: string; description?: string; tags?: string[] }) =>
    request<MarketplacePackage>(`/marketplace/packages/${id}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  deletePackage: (id: string) =>
    request<void>(`/marketplace/packages/${id}`, { method: "DELETE" }),

  publishVersion: (
    id: string,
    body: { version: string; manifest: object; changelog?: string; oss_key?: string }
  ) =>
    request<MarketplacePackageVersion>(`/marketplace/packages/${id}/versions`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  // Send a local directory path; Go reads the filesystem, validates, packs into tar.gz, and uploads.
  uploadSkillDir: (id: string, dirPath: string) =>
    request<{ oss_key: string; skill_md: string }>(`/marketplace/packages/${id}/skill-dir-upload`, {
      method: "POST",
      body: JSON.stringify({ dir: dirPath }),
    }),

  listInstalledMCPs: (projectId?: string) => {
    const qs = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<InstalledMCP[]>(`/marketplace/installed-mcps${qs}`);
  },

  installMCP: (body: { name: string; package_id?: string; version?: string; mcp_key: string; manifest: object; env?: Record<string, string>; project_id?: string }) =>
    request<InstalledMCP>("/marketplace/installed-mcps", {
      method: "POST",
      // Server stores manifest as a JSON string; pre-serialize the object here
      body: JSON.stringify({ ...body, manifest: JSON.stringify(body.manifest) }),
    }),

  uninstallMCP: (name: string, projectId?: string) => {
    const qs = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<void>(`/marketplace/installed-mcps/${encodeURIComponent(name)}${qs}`, {
      method: "DELETE",
    });
  },

  listInstalledSkills: (projectId?: string) => {
    const qs = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<InstalledSkill[]>(`/marketplace/installed-skills${qs}`);
  },

  installSkill: (body: { name: string; package_id: string; version: string; project_id?: string }) =>
    request<InstalledSkill>("/marketplace/installed-skills", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  uninstallSkill: (name: string, projectId?: string) => {
    const qs = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    return request<void>(
      `/marketplace/installed-skills/${encodeURIComponent(name)}${qs}`,
      { method: "DELETE" }
    );
  },

  // Stars
  getStarStatus: (id: string) =>
    request<PackageStarStatus>(`/marketplace/packages/${id}/star`),

  starPackage: (id: string) =>
    request<PackageStarStatus>(`/marketplace/packages/${id}/star`, { method: "POST" }),

  unstarPackage: (id: string) =>
    request<PackageStarStatus>(`/marketplace/packages/${id}/star`, { method: "DELETE" }),

  // Comments
  listComments: (id: string) =>
    request<PackageReview[]>(`/marketplace/packages/${id}/reviews`),

  addComment: (id: string, body: { body: string }) =>
    request<PackageReview>(`/marketplace/packages/${id}/reviews`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  deleteComment: (id: string, commentId: string) =>
    request<void>(`/marketplace/packages/${id}/reviews/${commentId}`, { method: "DELETE" }),
};
