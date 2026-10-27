# Deployment, Configuration, and Maintenance

NexBox runs on your own computer or server. A single Go process serves the API, WebSocket, and embedded frontend, while SQLite stores local data. Agent CLIs and terminals run under the same system account.

## Local Usage

Download the archive matching your platform from the [latest release](https://github.com/esonic/nexbox/releases/latest), extract it into a writable directory, and run:

```bash
chmod +x nexbox
./nexbox
```

To build from source instead, see the [development guide](development.md#prerequisites).

By default, NexBox listens on `127.0.0.1:9040`. A new installation requires no account or sign-in. The frontend is embedded in the executable, so a separate Node.js service is unnecessary. The default data directory is the executable's directory. For source builds, the default executable is `dist/nexbox`, so its data is stored in `dist/` unless overridden.

## Using an Existing Executable

Place a `nexbox` executable matching the target platform in any writable directory and run it directly. No installer, startup script, or source code is required. If it lacks execute permission, run `chmod +x nexbox` first.

For example, after placing it at `/opt/nexbox/nexbox`:

```bash
/opt/nexbox/nexbox --help
/opt/nexbox/nexbox
```

You can also store the executable and data separately:

```bash
/opt/nexbox/nexbox list-users --data-dir /path/to/data
/opt/nexbox/nexbox --data-dir /path/to/data
```

New installations allow passwordless access. Add an account if you need password login, using the same data directory for account commands and the service.

## Server Deployment

The target machine needs Git and the CLIs, authentication, or provider configuration for the agents you use. Chrome/Chromium is required only for the managed browser. Running a prebuilt NexBox executable does not require Go, Node.js, or npm. The executable must match the target operating system and CPU architecture.

Verify each agent CLI under the same system account that runs the service. Service managers usually do not read interactive shell configuration, so explicitly set the required `PATH` and environment variables.

For public domain access or sharing with trusted users, add an account as described under [Local Accounts](#local-accounts) to enable password login. The access mode depends only on whether login accounts exist; it does not change automatically based on the listening address or domain.

Listen on a remote address directly:

```bash
NEXBOX_DATA_DIR=/var/lib/nexbox /opt/nexbox/nexbox --listen 0.0.0.0 --port 9040
```

Alternatively, keep the default loopback address and use an existing SSH tunnel or HTTPS reverse proxy. The proxy must forward WebSocket connections and allow long-running streamed responses. Disable response buffering for conversation connections and avoid short idle timeouts.

The NexBox service provides HTTP. Configure TLS for public access at the reverse proxy. The runtime is intended for personal or trusted-user use; all agents and terminals share the service account's system permissions.

Use your preferred service manager for startup at boot or process supervision. NexBox does not require a particular startup script.

## Runtime Options

| Option | Default | Meaning |
| --- | --- | --- |
| `--listen` | `127.0.0.1` | HTTP listening address |
| `--port` | `9040` | HTTP port |
| `--data-dir` | See below | Service data directory |
| `--version` | — | Print the build version and exit |
| `--help` | — | Show service options and account commands |

Data directory precedence:

1. Explicit `--data-dir` option.
2. `NEXBOX_DATA_DIR` environment variable.
3. Directory containing the executable.

The same precedence applies to account subcommands. Place `--data-dir` before the subcommand or immediately after it, before positional arguments:

```bash
./nexbox --data-dir /path/to/data list-users
./nexbox add-user --data-dir /path/to/data local "$NEXBOX_PASSWORD"
```

## Local Accounts

Accounts are optional. Without login accounts, all visitors enter the same local workspace directly, and the interface hides account and sign-out controls.

```bash
./nexbox list-users
./nexbox add-user local "$NEXBOX_PASSWORD"
./nexbox reset-password local "$NEXBOX_PASSWORD"
./nexbox del-user local
```

Before creating or resetting a password, set `NEXBOX_PASSWORD` through hidden terminal input, then run `unset NEXBOX_PASSWORD` afterward. Account names must contain 1–32 ASCII letters, digits, underscores, or hyphens.

Adding the first account immediately enables password login and assigns existing projects and sessions to that account without changing file paths. Additional accounts create their own projects. Deleting the last account returns all projects to the passwordless workspace. Adding or deleting accounts requires no restart. Mode changes disconnect open conversation and terminal connections, and the page rechecks login status.

Accounts are managed through local commands, with no external registration, email service, or administrator role configuration. Instances with existing accounts still require login after upgrades. When password login is enabled, restarting the service invalidates existing logins and requires users to sign in again.

## Application Configuration

The first startup creates `<data-dir>/config.json`. Common options can be changed in the interface. Example configuration:

```json
{
  "default_agent": "claude",
  "chrome_headless": true,
  "mcp_tools": {
    "task_management": false
  },
  "suggest_next_actions": true,
  "prompt_templates": []
}
```

| Setting | Meaning |
| --- | --- |
| `default_agent` | Default agent for new sessions and lightweight helper tasks |
| `chrome_headless` | Use headless mode for newly created managed Chrome instances |
| `mcp_tools.task_management` | Expose built-in task management tools to agents; disabled by default |
| `suggest_next_actions` | Request clickable follow-up suggestions at the end of replies; enabled by default |
| `prompt_templates` | Global prompt templates; each entry contains `name` and `content` |

Model services, authentication, and native agent configuration are managed by the corresponding CLI rather than this file. Restart the service after editing `config.json` manually. Settings changed in the interface take effect according to each feature's behavior. The current storage backend is SQLite.

To give Claude CLI a separate temporary directory, set `NEXBOX_CLAUDE_TMPDIR`. Its value is passed unchanged to the CLI's `CLAUDE_CODE_TMPDIR`.

### Optional Site Verification and Android TWA

Only public deployments that need domain ownership verification or Android Trusted Web Activity require `public_web`. Regular local and server deployments do not need these fields. When configured, NexBox serves the corresponding root-level verification files and `/.well-known/assetlinks.json`:

```json
{
  "public_web": {
    "verification_files": {
      "provider-verification.txt": "replace-with-your-verification-value"
    },
    "android_asset_links": {
      "package_name": "com.example.app",
      "sha256_cert_fingerprints": ["AA:BB:CC:..."]
    }
  }
}
```

Store these deployment values in the instance's existing `config.json`; no extra configuration file is needed. Without configuration, the corresponding URLs do not publish verification content.

## Data and Working Directories

| Path | Contents |
| --- | --- |
| `nexbox.db` | Accounts, projects, sessions, messages, workflows, marketplace installations, and other records |
| `config.json` | Application settings |
| `logs/` | Runtime logs, rotated by size |
| `users/` | Project working directories created automatically by NexBox |
| `sessions/` | Session attachments and related files |
| `worktrees/` | Git worktrees created by sessions or workflows |
| `marketplace/` | Marketplace sources and local copies |
| `global-plugins/` | Global plugin installation directory, created on demand |
| `chrome/` | Managed Chrome user data |
| `crush/` | Adapter runtime data when using Crush |

All paths above are relative to the data directory. User-selected project paths can be outside it. Native agent session and login data may also be stored in the agent's own home directory. Saving only `nexbox.db` does not include those external files.

Projects can create directories automatically or connect to existing directories. Connecting an existing path does not copy its files into the data directory.

## Upgrades and Shutdown

1. Stop NexBox and retain the existing data directory.
2. Replace the executable with the new version matching the target platform, or update the source and run `./build.sh`.
3. Start NexBox with the same data directory. SQLite migrations run at startup.

Stop the existing process before replacing the executable, then restart after the update. Use `Ctrl+C` for a foreground process or the normal stop command for your service manager. NexBox handles SIGINT/SIGTERM and closes browser and MCP resources. Data migrations do not guarantee downgrade compatibility.

## Troubleshooting

**Cannot find the build output**: The default output is `dist/nexbox`. If `NEXBOX_OUTPUT` was set, run the executable at that location.

**Cannot log in after creating an account, or existing data is missing**: Confirm that account commands and the service use the same executable and data directory. In particular, check whether the service's `--data-dir` overrides `NEXBOX_DATA_DIR`.

**Agent not found or unable to use a model**: Confirm that the CLI is available under the service account and `PATH`, and can complete a request independently. Some CLIs are registered only at startup; restart NexBox after installation.

**Browser tools fail to start**: Confirm Chrome/Chromium is installed. Servers should generally keep `chrome_headless: true`. For external Chrome, provide a DevTools address reachable by the NexBox service process.

**Remote conversations or terminals disconnect**: Check the reverse proxy's WebSocket forwarding, response buffering, and connection timeouts. Access the NexBox listening port directly first to distinguish service issues from proxy issues.
