# E2E Test Progress

> Refer back to docs/e2e-test-cases.md for full test case details.
> Update this file as tests are completed.

## Bugs Found & Fixed

| # | Severity | Description | File | Status |
|---|----------|-------------|------|--------|
| 1 | Critical | Local mode 401 on all API calls - AuthMiddleware/WSAuthMiddleware missing JWTSecret=="" bypass | server/api/middleware.go | FIXED |
| 2 | Minor | Excessive diff-stats polling (10s -> 30s) | web/src/components/chat/ChatPanel.tsx | FIXED |

## Test Results

### TS-1: Project Creation UX
- [ ] TC-1.1: Create project with name and description
- [ ] TC-1.2: Create project with empty name rejected
- [ ] TC-1.3: Edit project name and description
- [ ] TC-1.4: Delete project with confirmation
- [ ] TC-1.5: Delete project - cancel flow

### TS-2: Session Management & Limits
- [ ] TC-2.1: Create session within limit
- [ ] TC-2.2: Session limit reached (skip - needs 20 sessions)
- [ ] TC-2.3: Restore session blocked at limit (skip - needs 20 sessions)
- [ ] TC-2.4: Delete session (move to trash)
- [ ] TC-2.5: Restore session from trash

### TS-3: Trash & Soft Delete Cleanup
- [ ] TC-3.1: Trash hint text shows 1 day

### TS-4: Message Limit
- [ ] TC-4.1: Messages display within limit

### TS-5: Balance Display
- [ ] TC-5.1: Balance shown in sidebar (local mode - may be 0)
- [ ] TC-5.2: Balance hidden when zero

### TS-6: Model Selection
- [ ] TC-6.1: Model selector visible in chat input
- [ ] TC-6.2: Switch model to Haiku
- [ ] TC-6.3: Switch model to Sonnet
- [ ] TC-6.4: New session uses default model

### TS-7: Remove / and @ Special Interactions
- [ ] TC-7.1: Typing / does not show command menu
- [ ] TC-7.2: Typing @ does not show mention menu

### TS-8: File Upload & Download
- [ ] TC-8.1: Upload a file
- [ ] TC-8.2: Download a file

### TS-9: Files / Terminal / Git Toolbar Buttons
- [x] TC-9.1: Files button in toolbar (PASS - visible in top right)
- [x] TC-9.3: Git button in toolbar (PASS - visible in top right)

### TS-10: Marketplace - Local Only
- [x] TC-10.1: Marketplace page loads (PASS - shows Connectors/Skills/Plugins tabs, 0 available)
- [ ] TC-10.2: No cloud packages API calls

### TS-11: Marketplace - Anthropic Repository Format
- [ ] TC-11.1: Plugins from different formats displayed (needs sample data)

### TS-12: Connector Format Support
- [ ] TC-12.1: Connector-type plugins displayed (needs sample data)

### TS-13: Container Resource Limits
- [ ] TC-13.1: (backend-only, skip in browser E2E)

### TS-14: Container Idle Timeout
- [ ] TC-14.1: (backend-only, skip in browser E2E)

### TS-15: Project Delete Cleans Directory
- [ ] TC-15.1: Project deletion cleanup

### TS-16: SQLite Maintenance
- [ ] TC-16.1: (backend-only, skip in browser E2E)

### TS-17: Claude CLI Interactive Response
- [ ] TC-17.1: AskUserQuestion card rendered (needs live agent)
- [ ] TC-17.2: User responds to AskUserQuestion (needs live agent)

### TS-18: Login & Auth Flow
- [ ] TC-18.1: Login page displayed when token missing
- [ ] TC-18.2: Successful login with verification code
- [ ] TC-18.3: Logout flow
- [ ] TC-18.4: Token expiry handling

### TS-19: Settings Page
- [x] TC-19.1: Settings page accessible (PASS - shows Language/Agent/GitLab Token)
- [ ] TC-19.2: Language switch (i18n)

### TS-20: Sidebar Navigation
- [x] TC-20.2: Sidebar navigation links (PASS - Marketplace, Settings, session click all work)
- [ ] TC-20.1: Sidebar collapse/expand
- [ ] TC-20.3: Project grouping in sidebar
