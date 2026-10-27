# Agent Support

NexBox invokes agent CLIs on the machine where it runs. It does not include a model service or replace CLI authentication and provider configuration. Configure each CLI under the same system account that runs NexBox.

## Integrations

| Agent | CLI | Protocol | Model switching within a session | Forking | Follow-up messages while running |
| --- | --- | --- | --- | --- | --- |
| Claude Code | `claude` | SDK control / stream-json | Supported | Supported | Supported |
| Codex | `codex` | app-server JSON-RPC | Supported | Supported | Supported |
| Pi | `pi` | RPC + NexBox extension | Supported | Supported | Supported |
| Kimi Code | `kimi` | ACP stdio | Supported | Latest completed turn only | Queued |
| ZeroClaw | `zeroclaw` | ACP stdio | New sessions only | Not supported | Queued |
| Crush | `crush` | Native HTTP / SSE | Supported | Not supported | Queued |

The interface follows the capabilities declared by each adapter. CLI versions, models, and native capabilities may differ. When reporting compatibility issues, include the CLI version and NexBox build version.

Kimi, ZeroClaw, and Crush executables are detected at startup and are not registered if missing. Claude Code, Codex, and Pi also require their respective CLIs to start conversations. Restart NexBox after installing a CLI or changing the service process environment.

## Models and Configuration

- **Claude Code / Codex**: Use native model catalogs; authentication is managed by the CLI.
- **Pi**: Reads Pi configuration and the selected model catalog. When no catalog is explicitly configured, Pi uses its default model. The NexBox extension supports injecting HTTP MCP connections; stdio MCP is not currently supported through this extension.
- **Kimi Code**: Model aliases come from `kimi provider list --json`. Kimi manages the default model and session persistence. ACP has no historical turn selector, so exact forks are available only from the latest completed turn.
- **ZeroClaw**: Model selector entries correspond to agent aliases from `zeroclaw agents list`. ZeroClaw determines each alias's model, provider, and execution policy. Aliases are passed unchanged to the native protocol, and multiple aliases can be configured.
- **Crush**: Reads providers and models from native configuration rather than a full online model catalog. Existing sessions support model switching. NexBox manages the native service process on demand.

Optional CLI path environment variables:

| Variable | Purpose |
| --- | --- |
| `NEXBOX_PI_CLI` | Pi executable |
| `NEXBOX_KIMI_CLI` | Kimi executable |
| `NEXBOX_ZEROCLAW_CLI` | ZeroClaw executable |
| `NEXBOX_ZEROCLAW_AGENT` | Explicit ZeroClaw alias selection |
| `NEXBOX_CRUSH_CLI` | Crush executable |

Native CLI environment variables follow each CLI's own rules. Manage authentication and model configuration through the corresponding CLI. Do not put personal credentials in source code or committed configuration examples.

## Sessions and Forking

NexBox session IDs differ from native agent session IDs. Adapters store native continuation identifiers and, where supported, checkpoints for completed responses.

Forking copies conversation history up to the selected response, but inherits the parent session's **current** working directory, including uncommitted and untracked files. It does not roll back files or create another branch or worktree. Subsequent conversations are independent, while file changes remain visible to both sessions.

## Browser Tools and Background Tasks

The Claude Code, Codex, and Kimi adapters currently declare Chrome tool support. Sessions can use a NexBox-managed browser or connect to an external Chrome instance.

Visible subagent events depend on the native protocol. Kimi ACP does not expose subagent internals; its background tasks do not survive across turns after the per-turn process exits. Capabilities are not identical across backends.
