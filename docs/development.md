# Development Guide

## Prerequisites

For regular use, download a release without building from source; see the [README quick start](../README.md#quick-start). Building and developing from source requires:

| Component | Requirement |
| --- | --- |
| Operating system | Linux or macOS; use WSL2 on Windows |
| Go | 1.25 or later |
| Node.js / npm | Node.js 22.12+ and npm 9+ |
| Git | Required to obtain the source and use Git features |

Clone the repository and enter the project directory:

```bash
git clone https://github.com/esonic/nexbox.git
cd nexbox
```

Build and start the complete application:

```bash
./build.sh
./dist/nexbox
```

The build script runs `npm ci`. For frontend-only development, you can also run `npm ci --prefix web` manually. Go modules are downloaded automatically by `go run`, `go build`, and `go test`.

## Development Servers

Run the following commands from the repository root to use a separate development data directory and complete the initial build. New data directories allow passwordless access by default. When testing accounts, account commands and the service must use the same directory:

```bash
export NEXBOX_DATA_DIR="$HOME/nexbox-dev-data"
./build.sh
```

Start the Go service in one terminal:

```bash
cd server
NEXBOX_DATA_DIR="$HOME/nexbox-dev-data" go run .
```

Start the frontend in another terminal:

```bash
cd web
npm run dev
```

The default frontend address is `http://localhost:5173`. Vite proxies `/api` and `/ws` to `localhost:9040`. If you change the backend port, set `PORT` in the terminal running the frontend:

```bash
PORT=9050 npm run dev
```

`PORT` controls only the development proxy target; use `--port` for the Go service. Use the actual frontend port printed by Vite.

## Builds

```bash
./build.sh
./dist/nexbox --version
```

The build generates the frontend, updates `server/webdist/`, and embeds the assets in a single Go executable. `server/webdist/.gitkeep` lets Go packages compile from a clean checkout; generated frontend assets are not committed.

Override the output path if needed:

```bash
NEXBOX_OUTPUT=dist/nexbox-preview ./build.sh
./dist/nexbox-preview
```

Relative output paths are always resolved against the repository root. The default version comes from `git describe --tags --always --dirty`; source archives without Git metadata use `dev`.

## Verification

```bash
cd server
go test ./...
```

The default backend tests do not launch a host browser and are suitable for local development and regular CI. Real Chrome/Chromium end-to-end tests require an installed browser and explicit opt-in:

```bash
cd server
NEXBOX_CHROME_E2E=1 go test -v -count=1 -timeout=10m ./mcp/chrome
```

The repository's **Browser E2E** workflow can also be run manually from GitHub Actions. Tests that require authenticated CLIs or real models remain opt-in through the environment variables documented in their test files. Regular tests do not require a model service.

```bash
cd web
npm test
npm run build
npm run lint
```

`npm test` covers passwordless routes, account interfaces, session ordering, Git refresh behavior, and marketplace installation scopes. The repository still has existing ESLint issues; check affected files before submitting changes and avoid introducing new warnings.

Native MCP integration tests use temporary configuration and local model/MCP test servers. They require the corresponding CLIs but do not call real models:

```bash
cd server
MCP_NATIVE_INTEGRATION=1 go test ./api -run '^TestNative(Codex|Pi|Claude|Kimi)' -v
```

## Code Structure

| Path | Responsibility |
| --- | --- |
| `web/src/components/` | Conversations, files, Git, terminals, and shared UI |
| `web/src/hooks/`, `web/src/stores/` | Frontend state and interaction coordination |
| `server/api/` | HTTP / WebSocket, projects, and session scheduling |
| `server/agent/` | Machine protocol adapters for each agent |
| `server/store/` | SQLite and local data models |
| `server/gitrepo/` | Repository discovery, scope resolution, and Git workspaces |
| `server/mcp/` | Built-in MCP and browser tools |
| `server/workflow/` | Workflow graphs and execution engine |

Keep agent differences inside adapters when adding capabilities. The interface uses capability declarations for forking, follow-up messages while running, model switching, and browser support; it must not infer behavior from display names.
