package api

import (
	"os"
	"path/filepath"
	"testing"
)

// compactTestMkdirAll / compactTestWriteFile keep the tree fixtures below readable.
func compactTestMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func compactTestWriteFile(t *testing.T, path string) {
	t.Helper()
	compactTestMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// listDirLevel compacts chains of single-child directories into one entry
// (VSCode's compact folders), keeping the joined name and the innermost path.
func TestListDirLevelCompactsSingleChildDirs(t *testing.T) {
	root := t.TempDir()

	// a/b/c/file.txt — a full three-link chain ending at a folder with a file.
	compactTestWriteFile(t, filepath.Join(root, "a/b/c/file.txt"))
	// d holds two entries, so it must stay on its own row.
	compactTestWriteFile(t, filepath.Join(root, "d/f.txt"))
	compactTestMkdirAll(t, filepath.Join(root, "d/e"))
	// g/h has both i and node_modules, so compaction must stop at h.
	compactTestMkdirAll(t, filepath.Join(root, "g/h/i/j1"))
	compactTestMkdirAll(t, filepath.Join(root, "g/h/i/j2"))
	compactTestMkdirAll(t, filepath.Join(root, "g/h/node_modules"))
	// k holds only a file, so there is nothing to compact into.
	compactTestWriteFile(t, filepath.Join(root, "k/only.txt"))
	compactTestWriteFile(t, filepath.Join(root, "z.txt"))

	// Dependency/build directories and dot entries are browsable; Git metadata stays hidden.
	for _, name := range []string{"node_modules", "vendor", "dist", "build", ".agents", ".github", ".venv", ".next", ".idea", ".vscode", "__pycache__"} {
		compactTestMkdirAll(t, filepath.Join(root, name))
	}
	compactTestWriteFile(t, filepath.Join(root, ".gitignore"))
	compactTestWriteFile(t, filepath.Join(root, ".hidden"))
	compactTestWriteFile(t, filepath.Join(root, ".git/config"))

	entries, err := listDirLevel(root, root)
	if err != nil {
		t.Fatalf("listDirLevel: %v", err)
	}

	got := map[string]string{} // name -> path
	for _, e := range entries {
		got[e.Name] = e.Path
	}
	want := map[string]string{
		"a/b/c":        "a/b/c",
		"d":            "d",
		"g/h":          "g/h",
		"k":            "k",
		"z.txt":        "z.txt",
		"node_modules": "node_modules",
		"vendor":       "vendor",
		"dist":         "dist",
		"build":        "build",
		".agents":      ".agents",
		".github":      ".github",
		".venv":        ".venv",
		".next":        ".next",
		".idea":        ".idea",
		".vscode":      ".vscode",
		"__pycache__":  "__pycache__",
		".gitignore":   ".gitignore",
		".hidden":      ".hidden",
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for name, path := range want {
		if got[name] != path {
			t.Errorf("entry %q path = %q, want %q", name, got[name], path)
		}
	}

	// The compacted row's path is what the client expands, so listing it must
	// return the innermost folder's children.
	children, err := listDirLevel(root, filepath.Join(root, "a/b/c"))
	if err != nil {
		t.Fatalf("listDirLevel(a/b/c): %v", err)
	}
	if len(children) != 1 || children[0].Name != "file.txt" {
		t.Errorf("children of a/b/c = %v, want [file.txt]", children)
	}
}

// A symlinked directory is never merged, in either direction: following one
// could walk out of the tree or loop back into it.
func TestListDirLevelSkipsSymlinkedDirs(t *testing.T) {
	root := t.TempDir()
	compactTestMkdirAll(t, filepath.Join(root, "real/inner/deep"))
	compactTestMkdirAll(t, filepath.Join(root, "host"))
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// host's only child is a symlink to a directory — compaction must stop at host.
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "host/aliased")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	entries, err := listDirLevel(root, root)
	if err != nil {
		t.Fatalf("listDirLevel: %v", err)
	}
	for _, e := range entries {
		switch e.Path {
		case "link":
			if e.Name != "link" {
				t.Errorf("symlinked dir compacted to %q", e.Name)
			}
		case "host":
			if e.Name != "host" {
				t.Errorf("dir with symlinked child compacted to %q", e.Name)
			}
		case "real/inner/deep":
			// expected: the plain chain still compacts
		default:
			t.Errorf("unexpected entry %+v", e)
		}
	}
}
