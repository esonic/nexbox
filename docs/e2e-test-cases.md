# BeeHub E2E Test Cases

> Based on completed tasks from todo.md, todo2.md, todo3.md.
> Test framework: Playwright (recommended) or similar browser automation.
> Base URL: `http://localhost:<port>` (configurable)

---

## Prerequisites

- BeeHub server running (with valid Claude API key or mock)
- At least one user account available for login
- Docker daemon running (for container-related tests)
- `data/marketplace/` directory with sample plugin JSON, SKILL.md, and .claude-plugin files

---

## TS-1: Project Creation UX (T4)

> Project creation only requires Name + Description, no git_repo_path.

### TC-1.1: Create project with name and description

**Steps:**
1. Login and navigate to sidebar
2. Click "New project" button (FolderPlus icon) near Sessions header
3. In the ProjectDialog, enter Name: "Test Project"
4. Enter Description: "E2E test project"
5. Click submit button

**Expected:**
- Dialog only shows Name and Description fields (no path input)
- Project is created successfully, appears in sidebar project list
- Toast success message appears
- New project is selected as active project

### TC-1.2: Create project with empty name rejected

**Steps:**
1. Open ProjectDialog
2. Leave Name empty, click submit

**Expected:**
- Submit button is disabled or form does not submit
- No API call is made

### TC-1.3: Edit project name and description

**Steps:**
1. Click "..." menu on an existing project in sidebar
2. Select edit option
3. Change name and description
4. Submit

**Expected:**
- ProjectDialog opens in edit mode with pre-filled values
- After submit, sidebar reflects updated name

### TC-1.4: Delete project with confirmation

**Steps:**
1. Click "..." menu on project > "Delete Project"
2. ProjectDialog opens with delete confirmation section visible
3. Type the project name in confirmation input
4. Click delete button

**Expected:**
- Delete button is disabled until confirmation input matches project name exactly
- After deletion, project is removed from sidebar
- All sessions under the project are removed
- API call: `DELETE /api/projects/{id}`

### TC-1.5: Delete project - cancel flow

**Steps:**
1. Open delete confirmation for a project
2. Click cancel or close dialog

**Expected:**
- Project is not deleted
- Dialog closes normally

---

## TS-2: Session Management & Limits (T9.2)

> Max 20 active sessions per project.

### TC-2.1: Create session within limit

**Steps:**
1. Select a project with < 20 sessions
2. Click "New session" (SquarePen icon)

**Expected:**
- New session is created
- Session appears in sidebar session list
- Chat workspace switches to new session

### TC-2.2: Session limit reached - creation blocked

**Steps:**
1. Create sessions until there are 20 active sessions for one project
2. Try to create the 21st session

**Expected:**
- Error toast: something like "session limit reached" or "please delete old sessions"
- No new session is created

### TC-2.3: Restore session blocked when at limit

**Steps:**
1. With 20 active sessions in a project
2. Open trash (Recently Deleted)
3. Try to restore a trashed session

**Expected:**
- Restore fails with error message indicating active session limit reached
- Session remains in trash

### TC-2.4: Delete session (move to trash)

**Steps:**
1. Hover over a session in sidebar
2. Click delete (X icon) or use "Move to Trash" action
3. Confirm deletion if prompted

**Expected:**
- Session disappears from active list
- Session appears in "Recently Deleted" (trash)
- If active session was deleted, workspace switches to another session

### TC-2.5: Restore session from trash

**Steps:**
1. Open trash popover (Trash2 icon / "Recently Deleted")
2. Click restore button on a trashed session

**Expected:**
- Session returns to active list in sidebar
- Session is usable again

---

## TS-3: Trash & Soft Delete Cleanup (T9.1)

> Sessions are permanently deleted after 1 day (not 30 days).

### TC-3.1: Trash hint text shows 1 day

**Steps:**
1. Open trash popover in sidebar

**Expected:**
- Hint text reads: "Sessions are permanently deleted after 1 day."
- Verify in both EN and ZH locales if i18n is active

---

## TS-4: Message Limit (T9.3)

> Max 100 messages per session.

### TC-4.1: Messages display within limit

**Steps:**
1. Open a session with messages
2. Verify messages load via `GET /api/sessions/{id}/messages`

**Expected:**
- Messages are displayed in chat area
- API response includes `has_more` flag if applicable

### TC-4.2: Verify message limit enforcement (API level)

**Steps:**
1. Send messages to a session (can be verified via API)
2. After 100 messages, check that older messages are cleaned up (hourly cleanup)

**Expected:**
- Session never retains more than ~100 recent messages after cleanup cycle
- (Note: this is primarily a backend test; E2E verifies the chat still works)

---

## TS-5: Balance Display (T11)

> Sidebar shows token balance and CNY equivalent.

### TC-5.1: Balance shown in sidebar

**Steps:**
1. Login (user must have non-zero balance)
2. Observe sidebar area above Marketplace link

**Expected:**
- Balance displays as: `{N} tokens` (e.g., "1,234,567 tokens")
- If `balance_cny > 0`, shows: `{Y}{N.NN}` (e.g., "{Y}18.52")
- Numbers are formatted with locale separators (toLocaleString)

### TC-5.2: Balance hidden when zero

**Steps:**
1. Login as user with 0 balance

**Expected:**
- Balance section is not visible (conditional: `balanceTokens > 0`)

### TC-5.3: Balance refreshes after agent execution

**Steps:**
1. Note current balance
2. Send a message and wait for agent to complete (WS `settle` event)
3. Observe balance

**Expected:**
- Balance value refreshes automatically after agent execution completes
- No manual refresh needed

---

## TS-6: Model Selection (T12)

> Chat input shows model selector: Sonnet (default) / Haiku.

### TC-6.1: Model selector visible in chat input

**Steps:**
1. Open a session
2. Look at the chat input area (bottom of chat)

**Expected:**
- Model selector button is visible, showing current model label (default: "Sonnet")
- Clicking it opens dropdown with options: "Sonnet", "Haiku"

### TC-6.2: Switch model to Haiku

**Steps:**
1. Click model selector
2. Select "Haiku"

**Expected:**
- Selector label updates to "Haiku"
- Session model is updated (API: `PUT /api/sessions/{id}` with `model: "claude-haiku-4-5"`)
- Selected model has check mark in dropdown

### TC-6.3: Switch model to Sonnet

**Steps:**
1. With Haiku selected, click model selector
2. Select "Sonnet"

**Expected:**
- Selector label updates to "Sonnet"
- Model value: `claude-sonnet-4-6`

### TC-6.4: New session uses default model

**Steps:**
1. Create a new session

**Expected:**
- Model selector shows "Sonnet" (default)
- Session created with model `claude-sonnet-4-6` or empty (server default)

---

## TS-7: Remove / and @ Special Interactions (T13.1)

> Slash and @ commands removed. Input treated as plain text.

### TC-7.1: Typing / does not show command menu

**Steps:**
1. Open chat input
2. Type `/`

**Expected:**
- No slash command popup/menu appears
- `/` is treated as normal text character

### TC-7.2: Typing @ does not show mention menu

**Steps:**
1. Open chat input
2. Type `@`

**Expected:**
- No mention/autocomplete popup appears
- `@` is treated as normal text character

### TC-7.3: Send message with / prefix

**Steps:**
1. Type `/hello world` in chat input
2. Press Enter / Send

**Expected:**
- Message is sent as plain text `/hello world`
- No special command processing occurs

---

## TS-8: File Upload & Download (T7)

> Backend: POST upload, GET download. Frontend: file tree UI integration.

### TC-8.1: Upload a file

**Steps:**
1. Open Files panel (right sidebar)
2. Use upload UI (drag-and-drop or click upload)
3. Select a small text file (e.g., `test.txt`, < 1MB)

**Expected:**
- File uploads successfully
- API call: `POST /api/projects/{projectId}/files/upload` (multipart/form-data)
- File appears in file tree after refresh
- Success feedback (toast or in-panel indicator)

### TC-8.2: Download a file

**Steps:**
1. Open Files panel
2. Right-click on a file in the file tree
3. Click download

**Expected:**
- Browser initiates file download
- Download URL: `/api/projects/{projectId}/files/download?path=...`
- File content matches the original

### TC-8.3: Upload with path traversal rejected

**Steps:**
1. Attempt to upload a file with path containing `../` (via API or manipulated request)

**Expected:**
- Server rejects the request (400 or 403)
- File is not written outside project workspace

---

## TS-9: Files / Terminal / Git Toolbar Buttons (T6)

> Buttons moved to right sidebar top toolbar row.

### TC-9.1: Files button in toolbar

**Steps:**
1. Open a session workspace
2. Look at right sidebar top area

**Expected:**
- "Files" button is visible in the top toolbar row
- Clicking it opens the Files panel

### TC-9.2: Terminal button in toolbar

**Steps:**
1. Look at the toolbar area

**Expected:**
- Terminal button is visible (may be controlled by features bitmask)
- Clicking it toggles the terminal panel at the bottom

### TC-9.3: Git button in toolbar

**Steps:**
1. Look at the toolbar area

**Expected:**
- Git button is visible
- Clicking it opens the Git panel (shows status, branches, commits)

### TC-9.4: Ctrl+` toggles terminal

**Steps:**
1. Press `Ctrl+\`` keyboard shortcut

**Expected:**
- Terminal panel toggles open/closed
- Same behavior as clicking Terminal button

---

## TS-10: Marketplace - Local Only (T1.1)

> Cloud packages system removed. Only local plugin API remains.

### TC-10.1: Marketplace page loads local plugins

**Steps:**
1. Click "Marketplace" in sidebar
2. Wait for page to load

**Expected:**
- Page loads and displays plugins from `GET /api/marketplace/plugins`
- No cloud package tabs or cloud-related UI elements
- Plugins show: name, description, type (mcp/skill/plugin), version

### TC-10.2: No cloud packages API calls

**Steps:**
1. Open browser DevTools Network tab
2. Navigate to Marketplace page

**Expected:**
- No requests to `/api/marketplace/packages` (removed endpoint)
- Only requests to `/api/marketplace/plugins` and installed endpoints

---

## TS-11: Marketplace - Anthropic Repository Format (T1.2)

> Supports SKILL.md, .claude-plugin, and JSON formats.

### TC-11.1: Plugins from different formats displayed

**Steps:**
1. Ensure `data/marketplace/` contains:
   - `custom/*.json` (JSON format plugins)
   - `skills/*/SKILL.md` (Anthropic skill format)
   - `plugins/*/.claude-plugin/` (Anthropic plugin format)
2. Navigate to Marketplace page

**Expected:**
- All three format types are listed
- Each plugin shows correct metadata (name, description, type)
- `source` field distinguishes origin: "custom", "skill", "plugin"

### TC-11.2: Install a skill from marketplace

**Steps:**
1. On Marketplace page, find a skill-type plugin
2. Click install button

**Expected:**
- API call: `POST /api/marketplace/installed-skills`
- Plugin marked as installed in UI
- Appears in installed list: `GET /api/marketplace/installed-skills?project_id=...`

### TC-11.3: Uninstall a skill

**Steps:**
1. Find an installed skill
2. Click uninstall

**Expected:**
- API call: `DELETE /api/marketplace/installed-skills/{name}`
- Plugin no longer marked as installed

---

## TS-12: Connector Format Support (T1.4)

> .claude-plugin/plugin.json + .mcp.json formats supported.

### TC-12.1: Connector-type plugins displayed

**Steps:**
1. Ensure marketplace directory contains connector-format plugins
2. Navigate to Marketplace page

**Expected:**
- Connector plugins appear with `source: "connector"`
- `auth_type` field is shown if applicable (oauth/apikey/none)
- `mcp_config` data is available for MCP installation

### TC-12.2: Install MCP from connector

**Steps:**
1. Find a connector-type plugin with `type: "mcp"`
2. Click install
3. Fill in required env vars if prompted

**Expected:**
- API call: `POST /api/marketplace/installed-mcps` with manifest data
- MCP is registered for the current project

---

## TS-13: Container Resource Limits (T3.1)

> Containers have memory/cpu/pids/disk limits.

### TC-13.1: Verify container has resource limits (API/backend check)

**Steps:**
1. Create a project and trigger container creation (send a chat message)
2. Inspect running container: `docker inspect <container_id>`

**Expected:**
- Memory limit set (e.g., 4g)
- CPU limit set (e.g., 1.0)
- PIDs limit set (e.g., 200)
- (This is primarily a backend/infra test, not directly visible in UI)

---

## TS-14: Container Idle Timeout (T3.3)

> Containers stopped after 1h idle, auto-recreated when needed.

### TC-14.1: Container recreated after timeout

**Steps:**
1. Create a session, send a message (container starts)
2. Wait for idle timeout (1h, or reduce for test)
3. Send another message

**Expected:**
- Container is automatically recreated
- Agent execution proceeds normally
- No error visible to user (transparent recovery)

---

## TS-15: Project Delete Cleans Directory (T3.4 + T9.4)

> Deleting a project removes host directory via os.RemoveAll.

### TC-15.1: Project deletion cleanup

**Steps:**
1. Create a project "CleanupTest"
2. Upload a file to it (creates workspace directory)
3. Delete the project (with name confirmation)
4. Check that the host workspace directory no longer exists

**Expected:**
- `DELETE /api/projects/{id}` succeeds
- Host directory for this project is removed
- Container (if running) is stopped
- (Verify via server logs or filesystem check)

---

## TS-16: SQLite Maintenance (T9.5)

> Daily WAL checkpoint + VACUUM.

### TC-16.1: Verify maintenance runs (log check)

**Steps:**
1. Check server logs after 24h+ of operation

**Expected:**
- Log entries for `PRAGMA wal_checkpoint(TRUNCATE)` execution
- Log entries for `VACUUM` execution
- (Backend-only test; no UI verification needed)

---

## TS-17: Claude CLI Interactive Response - AskUserQuestion (T14)

> Full chain: tool_use -> WS -> frontend render -> user response -> tool_result.

### TC-17.1: AskUserQuestion card rendered

**Steps:**
1. Trigger an agent action that invokes `AskUserQuestion` (e.g., a permission request)
2. Observe chat area

**Expected:**
- A special interactive card (QuestionCard/ToolCallCard) is rendered
- Card shows the question text
- Options/buttons are displayed for user to choose

### TC-17.2: User responds to AskUserQuestion

**Steps:**
1. When AskUserQuestion card appears
2. Click one of the provided options (or type a free-form response)

**Expected:**
- User's choice is sent back as tool_result via WebSocket
- Agent continues execution based on user's response
- Card updates to show selected option (non-interactive after response)

### TC-17.3: AskUserQuestion with free text input

**Steps:**
1. When AskUserQuestion card appears with free text option
2. Type a custom response and submit

**Expected:**
- Custom text is sent as tool_result
- Agent processes the free text response

---

## TS-18: Login & Auth Flow

> JWT-based auth for cloud/SaaS mode.

### TC-18.1: Login page displayed when token missing

**Steps:**
1. Clear localStorage (remove `beehub_jwt`)
2. Navigate to app root

**Expected:**
- LoginModal is displayed
- Login and Register tabs are available

### TC-18.2: Successful login

**Steps:**
1. On LoginModal, enter valid email and password
2. Click Login

**Expected:**
- JWT token stored in `localStorage` as `beehub_jwt`
- App layout loads (sidebar + workspace)
- User info available in app state

### TC-18.3: Logout flow

**Steps:**
1. Click logout button in sidebar
2. Confirm logout

**Expected:**
- Token cleared from localStorage
- Redirected to LoginModal
- API call: `POST /api/auth/logout`

### TC-18.4: Token expiry handling

**Steps:**
1. Login with valid token
2. Simulate 401 response from API

**Expected:**
- Token is cleared automatically
- User is redirected to LoginModal
- `beehub:logout` event is dispatched

---

## TS-19: Settings Page

### TC-19.1: Settings page accessible

**Steps:**
1. Click "Settings" in sidebar

**Expected:**
- Settings page loads at `/settings`
- Shows: Language selector, Default Agent, GitLab Token sections

### TC-19.2: Language switch (i18n)

**Steps:**
1. On Settings page, change language from EN to ZH (or vice versa)

**Expected:**
- Interface language updates immediately
- Preference stored in `localStorage` as `beehub_lang`
- Sidebar labels, buttons, and hints reflect new language

---

## TS-20: Sidebar Navigation

### TC-20.1: Sidebar collapse/expand

**Steps:**
1. Click collapse button (PanelLeftClose icon)
2. Click expand button (PanelLeftOpen icon)

**Expected:**
- Sidebar collapses to minimal width
- Sidebar expands back to normal width
- State persists across navigation

### TC-20.2: Sidebar navigation links

**Steps:**
1. Click "Marketplace" in sidebar
2. Click "Settings" in sidebar
3. Click session to return to workspace

**Expected:**
- Each click navigates to correct route (`/marketplace`, `/settings`, `/`)
- Active state is visually indicated

### TC-20.3: Project grouping in sidebar

**Steps:**
1. Create multiple projects
2. Create sessions in different projects

**Expected:**
- Sessions are grouped under their respective project headers
- Project headers are collapsible (ChevronRight/ChevronDown)
- Unassigned sessions appear separately

---

## Test Priority

| Priority | Test Suite | Task | Notes |
|----------|-----------|------|-------|
| P0 | TS-1 | T4 | Core creation flow |
| P0 | TS-2 | T9.2 | Session limits |
| P0 | TS-6 | T12 | Model selection |
| P0 | TS-5 | T11 | Balance display |
| P0 | TS-18 | Auth | Login/logout |
| P1 | TS-8 | T7 | File upload/download |
| P1 | TS-10 | T1.1 | Marketplace local-only |
| P1 | TS-11 | T1.2 | Repository format |
| P1 | TS-7 | T13.1 | /@ removal |
| P1 | TS-9 | T6 | Toolbar buttons |
| P1 | TS-17 | T14 | AskUserQuestion |
| P2 | TS-3 | T9.1 | Trash hint text |
| P2 | TS-4 | T9.3 | Message limit |
| P2 | TS-12 | T1.4 | Connector format |
| P2 | TS-13 | T3.1 | Container resources |
| P2 | TS-14 | T3.3 | Container TTL |
| P2 | TS-15 | T3.4+T9.4 | Cleanup on delete |
| P3 | TS-16 | T9.5 | SQLite maintenance |
| P3 | TS-19 | Settings | Language/agent |
| P3 | TS-20 | Sidebar | Navigation UX |

---

## Selectors Reference (for test implementation)

Key UI elements and suggested selectors:

| Element | Selector Strategy |
|---------|------------------|
| Sidebar | `.flex.h-screen > div:first-child` or add `data-testid="sidebar"` |
| New session button | Button with SquarePen icon near Sessions |
| New project button | Button with FolderPlus icon |
| ProjectDialog | Dialog with title "New project" / "Edit project" |
| Project name input | Input inside ProjectDialog |
| Delete confirm input | Input for project name confirmation |
| Model selector | Button in chat input area showing "Sonnet"/"Haiku" |
| Model dropdown | Dropdown items with model labels |
| Chat input | Textarea at bottom of chat area |
| Balance display | Text containing "tokens" in sidebar |
| Trash popover | Button with Trash2 icon, popover with trash hint |
| Files button | Toolbar button for Files panel |
| Terminal button | Toolbar button for Terminal panel |
| Git button | Toolbar button for Git panel |
| Marketplace link | Sidebar link with Store icon |
| Settings link | Sidebar link with Settings icon |

> Recommendation: Add `data-testid` attributes to key interactive elements before implementing tests.
