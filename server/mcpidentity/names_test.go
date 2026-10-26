package mcpidentity

import (
	"regexp"
	"strings"
	"testing"
)

func TestPortableMCPNames(t *testing.T) {
	valid := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	seen := map[string]string{}
	for _, raw := range []string{"oklog", "OKLog User", "OKLog_User", "日志", "", "nexbox", "chrome", strings.Repeat("x", 200)} {
		server := ServerKey(raw)
		if server != ServerKey(raw) || len(server) > 32 || !valid.MatchString(server) {
			t.Fatalf("invalid or unstable key for %q: %q", raw, server)
		}
		if previous, exists := seen[server]; exists && previous != raw {
			t.Fatalf("%q and %q collide", previous, raw)
		}
		seen[server] = raw
		for _, tool := range []string{"query_logs", "query.logs", "query logs", "查询", strings.Repeat("a", 200)} {
			name := "mcp__" + server + "__" + ToolKey(server, tool)
			if !valid.MatchString(name) {
				t.Fatalf("invalid full function name: %q", name)
			}
		}
	}
	if ServerKey("oklog") != "oklog" || ToolKey("oklog", "query_logs") != "query_logs" {
		t.Fatal("existing portable names changed")
	}
	if ServerKey("nexbox") == "nexbox" || ServerKey("chrome") == "chrome" {
		t.Fatal("an installed package can shadow a built-in MCP")
	}
}
