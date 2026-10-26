// Package mcpidentity defines the names BeeHub exposes to every agent runtime.
package mcpidentity

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

var unsafeCharacters = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// ServerKey leaves existing portable identifiers intact. Changed identifiers
// include a stable digest so e.g. "OKLog User" and "OKLog_User" stay distinct.
// Reserve room for mcp__<server>__<tool> inside a 64-character function name.
func ServerKey(raw string) string {
	if raw == "nexbox" || raw == "chrome" {
		return portable(raw, 32, true)
	}
	return portable(raw, 32, false)
}

// ToolKey is the MCP-side alias, before the CLI adds its server namespace.
// The upstream tool's real name must still be used for tools/call.
func ToolKey(server, raw string) string {
	return portable(raw, 64-len("mcp__")-len(server)-len("__"), false)
}

func portable(raw string, limit int, force bool) string {
	if raw != "" && len(raw) <= limit && !unsafeCharacters.MatchString(raw) && !force {
		return raw
	}
	base := strings.Trim(unsafeCharacters.ReplaceAllString(raw, "_"), "_")
	if base == "" {
		base = "mcp"
	}
	digest := sha256.Sum256([]byte(raw))
	suffix := fmt.Sprintf("_%x", digest[:6])
	if len(base) > limit-len(suffix) {
		base = base[:limit-len(suffix)]
	}
	return base + suffix
}
