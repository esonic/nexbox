# NexBox

A personal workspace for using coding agents in your browser.

NexBox brings conversations, source files, Git, terminals, and workflows together in a single interface. It runs on your computer or your own server and directly invokes the agent CLIs configured on that machine. The frontend is bundled with the Go service, so only a single executable is needed at runtime—there is no separate frontend to deploy or desktop shell to install.

## Features

- **Multi-agent conversations**: Claude Code, Codex, Pi, Kimi Code, ZeroClaw, and Crush, with streaming text, reasoning, and tool calls.
- **Session management**: Resume and fork conversations, pin sessions, sort by activity, and send follow-up messages while an agent is running when supported by the backend.
- **Code workspace**: File browsing, search, a Monaco editor, tool-call diffs, and a multi-repository Git panel.
- **Terminal and browser**: An integrated terminal and optional Chrome browser tools.
- **Reusable capabilities**: Prompt templates, MCP, Skills, and a plugin marketplace, with both project-level and global installation.
- **Task automation**: Workflows, scheduling, and cross-session collaboration.

## Requirements

| Component | Requirement |
| --- | --- |
| Operating system | Linux or macOS; use WSL2 on Windows |
| Go | 1.25 or later; required only for builds |
| Node.js / npm | Node.js 22.12+ and npm 9+; required only for builds and frontend development |

For remote deployment instructions, see the [operations guide](docs/ops_manual.md).

## Quick Start

From the source directory, run:

```bash
./build.sh
```

The build script installs frontend dependencies according to the lockfile, builds the frontend, and embeds it in the Go executable. The resulting binary is written to `dist/nexbox`. The Go build automatically downloads any missing modules. If you already have the executable, you can run it directly without using the build script.

Start NexBox:

```bash
./dist/nexbox
```

Open <http://127.0.0.1:9040> to get started. No account or sign-in is required. Select a configured agent to start a conversation, or create a project and connect it to an existing source directory. Before using an agent for the first time, make sure its CLI can run independently under the same system account.

### Optional: Enable Password Login

For remote access or sharing with trusted users, you can add a local account:

```bash
read -r -s -p 'Password: ' NEXBOX_PASSWORD
printf '\n'
./dist/nexbox add-user local "$NEXBOX_PASSWORD"
unset NEXBOX_PASSWORD
```

Password login is enabled immediately after you add the first account, with no restart required. Existing projects and sessions are assigned to that account, while their files remain in place. Deleting the last account restores passwordless access. Instances that already have accounts will continue to require sign-in after an upgrade.

Accounts are local to this NexBox instance and do not require registration with an external service. For detailed commands, see [Local Accounts](docs/ops_manual.md#本地账号).

## Common Commands

```bash
./build.sh                         # Build dist/nexbox from source, including the frontend
./dist/nexbox --help               # Show server options and account commands
./dist/nexbox --port 9050          # Use a different port
./dist/nexbox list-users           # List local accounts
```

The executable can be placed in any writable directory. By default, the data directory is the directory containing the executable. You can also specify it explicitly:

```bash
export NEXBOX_DATA_DIR="$HOME/nexbox-data"
./dist/nexbox list-users
./dist/nexbox
```

Account commands and the server must use the same data directory. New data directories allow passwordless access by default. The `--data-dir` server option takes precedence over the environment variable.

Node.js, npm, Go, and the source directory are not required at runtime, but Git and the CLI for each agent you use must be installed. To deploy NexBox to another machine, copy an executable that matches its operating system and CPU architecture, then configure the agents on the target machine.

## Documentation

- [Deployment, Configuration, and Maintenance](docs/ops_manual.md): Local operation, server deployment, data directories, and upgrades.
- [Development Guide](docs/development.md): Development servers, builds, verification, and code structure.
- [Agent Support](docs/agents.md): CLIs, protocols, model selection, and capability limitations.
- [Architecture](docs/architecture.md): Processes, storage, and workspace semantics.
- [Marketplace and MCP](docs/marketplace.md): Sources, installation scopes, authorization, and availability.
