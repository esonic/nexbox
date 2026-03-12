package agent

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// containerName — pure logic, no Docker needed
// ---------------------------------------------------------------------------

func TestContainerName_SafeUUID(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440000"
	name := containerName(id)
	if !strings.HasPrefix(name, "boxnex-proj-") {
		t.Fatalf("expected boxnex-proj- prefix, got %s", name)
	}
	if !strings.Contains(name, id) {
		t.Fatalf("safe UUID should be used verbatim: got %s", name)
	}
}

func TestContainerName_UnsafePath(t *testing.T) {
	id := "/home/user/repo/my project"
	name := containerName(id)
	if !strings.HasPrefix(name, "boxnex-proj-") {
		t.Fatalf("expected boxnex-proj- prefix, got %s", name)
	}
	for _, c := range name {
		if c == '/' || c == ' ' {
			t.Fatalf("container name contains unsafe char %q: %s", c, name)
		}
	}
}

func TestContainerName_LongID(t *testing.T) {
	id := strings.Repeat("a", 49) // > 48 chars triggers hash
	name := containerName(id)
	if !strings.HasPrefix(name, "boxnex-proj-") {
		t.Fatalf("expected boxnex-proj- prefix, got %s", name)
	}
	// Should be hashed to 16 hex chars → total len = len("boxnex-proj-") + 16 = 28
	if len(name) != 28 {
		t.Fatalf("expected hashed name length 28, got %d: %s", len(name), name)
	}
}

func TestContainerName_Deterministic(t *testing.T) {
	id := "my-project-id"
	a, b := containerName(id), containerName(id)
	if a != b {
		t.Fatal("containerName must be deterministic")
	}
}
