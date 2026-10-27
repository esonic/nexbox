# Marketplace and MCP

The marketplace manages reusable MCP connections, Skills, and plugins. Sources can be local directories or Git repositories; individual HTTP MCP connections can also be added through the interface. NexBox does not include private or company-internal marketplace sources.

## Sources and Installation

Adding a source makes its contents browsable; it does not install every item. When installing, choose the global or project scope and provide the item's required configuration.

- **Global installations** apply to the current NexBox instance and are available to projects created later.
- **Project installations** apply only to the selected project and take precedence over global installations with the same name.
- The Install action on an installed item can adjust its scopes. Saving applies only the changes.
- Removing one scope preserves other installations. Removing a project override allows fallback to the global installation.

Deleting a source uninstalls items provided only by that source. Items with the same name still provided by other sources are retained. Installed items no longer listed in any source remain visible so they can be uninstalled manually.

## MCP Authorization

HTTP MCP uses headers from installation configuration; OAuth replaces Authorization when needed. The interface reports expired credentials or authorization. After completing authorization, send another message in the same session to retry the checks; deleting the session is unnecessary.

Before each turn, NexBox initializes installed HTTP MCP connections and discovers their tools. Only connectors that pass these checks are passed to the agent. Status values include:

| Status | Meaning |
| --- | --- |
| `ready` | HTTP initialization and tool discovery succeeded |
| `needs_auth` | OAuth authorization is required |
| `auth_rejected` | The server rejected the configured authorization |
| `unavailable` | Configuration, connection, protocol, or tool discovery failed |
| `runtime_managed` | A stdio connector whose availability is managed by the agent CLI |

Tool names are normalized to runtime constraints and mapped back to upstream names when called. Display names, installation scopes, and OAuth identities remain independent. The proxy preserves upstream 401 responses, accepts empty notification responses, and negotiates supported older MCP protocol versions.

## Agent Differences

Whether MCP connections, Skills, and plugins can run depends on the agent's native interface. Pi's NexBox extension currently supports only HTTP MCP. A stdio connector's `runtime_managed` status does not mean it has passed HTTP checks. Claude Code plugin directories and components do not automatically become native plugins supported by every backend.

Built-in task management tools are disabled by default and can be enabled in settings. Chrome tools are enabled per session; see [Agent Support](agents.md) for compatible agents.
