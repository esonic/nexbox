# NexBox

NexBox is a browser workspace for coding agents, running locally or on a user's
own server. It serves a React frontend from one Go executable and stores data
in SQLite. It is intended for individuals and trusted users; agent processes
and terminals run under the service's system account.

## Build and run

```bash
./build.sh                  # npm ci, frontend build, Go build -> dist/nexbox
./dist/nexbox --help        # server flags and local account commands
./dist/nexbox               # localhost:9040
```

See README.md for prerequisites and first-account creation. NEXBOX_DATA_DIR must
be identical for account commands and the server. Its default is the executable's
directory. Runtime needs neither build tools nor shell wrappers; users can copy
the executable to another machine with the same operating system and architecture.
Agent CLIs and their configuration must be available on that machine.

## Development

- Backend: `cd server && go run .`; use NEXBOX_DATA_DIR for an explicit dev data directory.
- Frontend: `cd web && npm run dev`; proxies /api and /ws to localhost:9040.
- Set PORT for the Vite proxy when the backend uses another port.
- Backend tests: `cd server && go test ./...`; full tests need Chrome/Chromium.
- Frontend checks: `cd web && npm test` and `npm run build`.
- ESLint has existing violations. Check affected files and avoid introducing new ones.

## Structure

- web/src/components: chat, files, Git, terminal, projects, and shared UI.
- web/src/hooks and stores: frontend state and interaction coordination.
- server/api: HTTP/WebSocket handlers and session dispatch.
- server/agent: Claude Code, Codex, Pi, Kimi Code, ZeroClaw, and Crush adapters.
- server/store: SQLite schema, migrations, and data access.
- server/gitrepo: repository discovery and scoped Git operations.
- server/mcp: built-in MCP and per-session browser tools.
- server/workflow: workflow execution.
- docs: maintained usage, deployment, architecture, and development documentation.

## Conventions

Keep CLI protocol differences inside agent adapters. Use declared capabilities
for fork, interjection, model switching, and browser support. Do not infer these
from an agent's display name. Preserve the distinction between NexBox session
IDs and native agent session IDs.

Forks share the parent's current workspace, including uncommitted files. They
do not restore files to the selected historical turn. Shared worktrees must stay
on disk while any retained session or active workflow references them.

Keep runtime data and generated assets out of Git. server/webdist/.gitkeep is
the only tracked embedded-frontend file. Do not reintroduce private deployment
paths, obsolete hosting plans, or configuration options absent from the runtime.
