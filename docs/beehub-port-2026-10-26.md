# BeeHub updates ported on 2026-10-26

Port baseline: NexBox `cloud` at `10eedd42`; source: BeeHub `main` at
`272467c`. Changes are adapted to NexBox's SQLite store, user-owned projects,
authentication, managed project directories, and existing sidebar controls.

## Included changes

| Area | BeeHub source commits | NexBox behavior |
| --- | --- | --- |
| Claude file diffs | `aaf78e8` | Accept SDK `tool_use_result` and transcript `toolUseResult`; render new-file Write diffs without inventing overwrite diffs. |
| Large diff previews | `a27d8d0` | Bound per-file server output and skip oversized UI previews; retain Git actions. |
| Browser tools | `832f624` | Handle selected-tab lifecycle, native dialogs, cancellation, timeouts, and external Chrome attachment consistently. |
| Marketplace cleanup | `6a82b6f` | Uninstall source-owned items when removing a source, protect items still offered elsewhere, and display orphaned installations. |
| Installation scopes | `ee618d2` | Edit installed scopes; distinguish all-scope removal from global-only removal; preserve remaining project installations and global fallbacks. |
| Session ordering | `bc8dafc`, `63fda76` | Persist pins and conversation activity; support manual, creation-time, and activity sorting, with project ordering. |
| Shared fork worktrees | `b46084e` (excluding Lark) | Share the parent's current workspace, lock inherited worktree mode, and preserve directories while sessions or active workflows still reference them. |
| Unpublished commits | `b46084e` (excluding Lark) | Detect unpublished commits without an upstream and retain dirty status when Git checks fail. |
| Git review refresh | `92bf0e6` | Publish complete Git snapshots, retain mounted previews during refresh, queue refreshes during mutations, and reject stale scope results. |
| Installed MCP runtime | `33833dc`, `608c34d` | Discover HTTP tools every turn, report connector availability, normalize tool names, translate calls, preserve upstream 401 responses, and support legacy protocol negotiation / notification acknowledgements. |
| Kimi Code | `2c2563a` (excluding Gateway) | Register `kimi` when its CLI is available; support streaming, models, resume, latest-turn forks, file diffs, interactive questions, MCP, images, and usage. |
| Chat presentation | `a8e9c5e`, `f6ead45`, `78908f0` | Expand Markdown tables, improve mobile composer/model labels, and preserve filenames in compact paths. |
| Project directory picker | `5d3e005` | Reuse the project dialog and directory picker while keeping automatic directory creation and user-supplied directory semantics. |

## Fork workspace semantics

A fork copies conversation history through the selected assistant reply. It
inherits the parent's current working directory, including uncommitted and
untracked files. It does not create a new branch or worktree and does not
restore the filesystem to the historical reply's state. Parent and child
sessions operate on the same files; their future conversation histories remain
separate. Forked sessions cannot change the inherited workspace mode.

Trash collection removes a shared worktree only after its last remaining
session or pending/running workflow reference is gone. Hidden sessions,
restorable trashed sessions, project subdirectories, and symlinked paths count
as references. Failed reference checks leave the worktree on disk.

Existing forks keep their existing directories; the new inheritance applies
to newly created forks. No existing worktree is moved or merged.

## Kimi setup and limitations

Install and authenticate Kimi Code using its own tooling, then restart NexBox.
The server registers `kimi` when it finds the CLI. `NEXBOX_KIMI_CLI` can select
another executable path. Providers, model aliases, login, and persistence stay
owned by Kimi (`KIMI_CODE_HOME`); NexBox does not rewrite its configuration.

The model catalog comes from `kimi provider list --json`. Existing sessions
can switch models and thinking levels. ACP has no mid-turn steering, so new
messages queue. Only the latest completed turn can be forked exactly; older
turn checkpoints are rejected. Background CLI tasks do not survive process
exit at the end of a NexBox turn. Child-agent internals are not streamed by ACP.

## Excluded changes

Lark Bee commands, BeeHub task fallback, BeeHub login callbacks, company LLM
Gateway configuration/error handling and agent variants, changing the default
agent to Codex, and GitLab/Prism CI configuration are excluded. NexBox's existing
login, default-agent choice, task integration, and deployment remain in place.

## Validation

Run from `server/`:

```sh
go test ./...
go test -race ./api ./store ./gitrepo ./mcpidentity -run 'TestMCP|TestPortable|TestForkSessionShares|TestPurgeTrash|TestDispatchUpdatesActivity|TestSession|Test.*Scope|Test.*Unpublished|Test.*Worktree'
MCP_NATIVE_INTEGRATION=1 go test ./api -run '^TestNative(Codex|Pi|Claude|Kimi)' -v
```

The native-client MCP checks use temporary configuration and loopback model/MCP
fixtures. They require installed CLIs and do not call production models. Browser
tests exercise managed and externally attached Chrome through Streamable HTTP.

Run from `web/`:

```sh
npm run build
npm run test:sessions
npm run test:git-review
npm run test:marketplace
```

The repository's full ESLint check has existing violations; compare against
`10eedd42` rather than treating them as introduced by this port.
