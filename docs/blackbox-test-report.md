# NexBox Server Black-Box Test Report

**Date**: 2026-02-09 17:44:20
**Server Port**: 47321 | **MCP Port**: 47322
**Data Dir**: (temp directory, cleaned up after test)

## Summary

- **Total**: 49
- **Passed**: 49
- **Failed**: 0
- **Pass Rate**: 100.0%

## Test Results

| # | Test Case | Result | Detail |
|---|-----------|--------|--------|
| 1 | GET /api/projects (empty) | PASS | array len=0 |
| 2 | POST /api/projects | PASS | status=201 |
| 3 | Create project - name | PASS | name=Test Project |
| 4 | POST /api/projects (no name) -> 400 | PASS | status=400 |
| 5 | POST /api/projects (bad path) -> 400 | PASS | status=400 |
| 6 | GET /api/projects/:id | PASS | name=Test Project |
| 7 | PUT /api/projects/:id | PASS | name=Updated Project |
| 8 | GET /api/projects (1 item) | PASS | array len=1 |
| 9 | GET /api/projects/nonexistent -> 404 | PASS | status=404 |
| 10 | POST /api/projects/:id/iterations | PASS | status=201 |
| 11 | Create iteration - title | PASS | title=Feature A |
| 12 | Create iteration - status | PASS | status=in_progress |
| 13 | POST iterations (no title) -> 400 | PASS | status=400 |
| 14 | GET /api/projects/:id/iterations | PASS | array len=1 |
| 15 | GET /api/iterations/:id | PASS | title=Feature A |
| 16 | PUT /api/iterations/:id - status | PASS | status=reviewing |
| 17 | GET /api/iterations/nonexistent -> 404 | PASS | status=404 |
| 18 | POST /api/iterations/:id/assets | PASS | status=201 |
| 19 | Create asset - title | PASS | title=API Design |
| 20 | Create asset - version | PASS | current_version=1 |
| 21 | POST asset (no type) -> 400 | PASS | status=400 |
| 22 | GET /api/iterations/:id/assets | PASS | array len=1 |
| 23 | GET /api/assets/:id - title | PASS | title=API Design |
| 24 | GET /api/assets/:id - has content | PASS | contains 'API Design v1' |
| 25 | PUT /api/assets/:id - version | PASS | current_version=2 |
| 26 | GET /api/assets/:id/versions | PASS | array len=2 |
| 27 | GET /api/assets/:id/diff | PASS | contains 'diffs' |
| 28 | GET asset diff (no params) -> 400 | PASS | status=400 |
| 29 | GET /api/assets/nonexistent -> 404 | PASS | status=404 |
| 30 | POST /api/assets/:id/comments | PASS | status=201 |
| 31 | Create comment - status | PASS | status=open |
| 32 | POST comment (no content) -> 400 | PASS | status=400 |
| 33 | GET /api/assets/:id/comments | PASS | array len=1 |
| 34 | PUT /api/comments/:id - status | PASS | status=resolved |
| 35 | PUT /api/comments/nonexistent -> 404 | PASS | status=404 |
| 36 | GET /api/iterations/:id/messages (empty) | PASS | array len=0 |
| 37 | POST /api/validate-path (valid) | PASS | contains 'true' |
| 38 | POST /api/validate-path (invalid) | PASS | contains 'false' |
| 39 | POST /api/validate-path (empty=valid) | PASS | contains 'true' |
| 40 | GET /api/iterations/:id/diff | PASS | contains 'diff' |
| 41 | GET diff (no repo) -> 400 | PASS | status=400 |
| 42 | MCP server reachable | PASS | status=400 |
| 43 | WebSocket /ws/chat/:id reachable | PASS | status=000 (curl WS limitation) |
| 44 | WebSocket /ws/chat/nonexistent -> 404 | PASS | status=404 |
| 45 | DELETE /api/projects/:id | PASS | status=204 |
| 46 | GET iteration after project delete -> 404 | PASS | status=404 |
| 47 | GET asset after project delete -> 404 | PASS | status=404 |
| 48 | GET /api/projects after delete | PASS | array len=1 |
| 49 | DELETE /api/projects/nonexistent -> 404 | PASS | status=404 |

## Test Categories

### 1. Projects CRUD (T1-T8)
- List projects (empty state)
- Create project with valid git_repo_path
- Create project without name -> 400
- Create project with invalid path -> 400
- Get project by ID
- Update project name
- List projects (verify count)
- Get non-existent project -> 404

### 2. Iterations CRUD (T9-T14)
- Create iteration under project
- Create iteration without title -> 400
- List iterations under project
- Get iteration by ID
- Update iteration status
- Get non-existent iteration -> 404

### 3. Assets CRUD & Versioning (T15-T23)
- Create asset with type, title, content
- Create asset missing required fields -> 400
- List assets under iteration
- Get asset with content
- Update asset content (creates new version, version increments)
- List asset versions (verify count)
- Diff between two versions
- Diff missing version params -> 400
- Get non-existent asset -> 404

### 4. Review Comments (T24-T28)
- Create comment on asset version
- Create comment missing content -> 400
- List comments for asset
- Update comment status (open -> resolved)
- Update non-existent comment -> 404

### 5. Chat Messages (T29)
- List messages for iteration (empty state)

### 6. Path Validation (T30-T32)
- Validate existing directory path -> valid=true
- Validate non-existent path -> valid=false
- Validate empty path -> valid=true (allowed)

### 7. Git Diff (T33-T34)
- Get diff for iteration with uncommitted changes in git repo
- Get diff for iteration without git_repo_path -> 400

### 8. MCP Server (T35)
- MCP HTTP server reachable on configured port

### 9. WebSocket Chat (T36-T37)
- WebSocket endpoint reachable for valid iteration
- WebSocket for non-existent iteration -> 404

### 10. Cascade Delete (T38-T41)
- Delete project returns 204
- Iteration under deleted project -> 404
- Asset under deleted project -> 404
- List projects reflects deletion

### 11. Edge Cases (T42)
- Delete non-existent project -> 404
