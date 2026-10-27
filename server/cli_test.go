package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAccountCommandDataDir(t *testing.T) {
	for _, placement := range []string{"before", "after", "before-equals", "after-equals", "single-dash", "default"} {
		t.Run(placement, func(t *testing.T) {
			root := t.TempDir()
			fallback := filepath.Join(root, "fallback")
			selected := filepath.Join(root, "selected directory")
			var args []string
			switch placement {
			case "before":
				args = []string{"--data-dir", selected, "list-users"}
			case "after":
				args = []string{"list-users", "--data-dir", selected}
			case "before-equals":
				args = []string{"--data-dir=" + selected, "list-users"}
			case "after-equals":
				args = []string{"list-users", "--data-dir=" + selected}
			case "single-dash":
				args = []string{"-data-dir", selected, "list-users"}
			case "default":
				args = []string{"list-users"}
				selected = fallback
			}
			handled, code := runCLISubcommandArgs(args, fallback)
			if !handled || code != 0 {
				t.Fatalf("handled=%v, code=%d", handled, code)
			}
			if _, err := os.Stat(filepath.Join(selected, "nexbox.db")); err != nil {
				t.Fatalf("database missing from selected directory: %v", err)
			}
			if selected != fallback {
				if _, err := os.Stat(fallback); !os.IsNotExist(err) {
					t.Fatalf("fallback directory should not be touched: %v", err)
				}
			}
		})
	}
}

func TestAccountCommandInvalidOptionsDoNotOpenDatabase(t *testing.T) {
	for _, args := range [][]string{
		{"list-users", "--data-dir"},
		{"list-users", "--data-dir="},
		{"list-users", "--unknown"},
		{"list-users", "unexpected"},
	} {
		t.Run(args[1], func(t *testing.T) {
			fallback := filepath.Join(t.TempDir(), "unused")
			handled, code := runCLISubcommandArgs(args, fallback)
			if !handled || code != 2 {
				t.Fatalf("handled=%v, code=%d", handled, code)
			}
			if _, err := os.Stat(fallback); !os.IsNotExist(err) {
				t.Fatalf("invalid options should not open a database: %v", err)
			}
		})
	}
}

func TestAccountCommandLeavesServerOptionsUntouched(t *testing.T) {
	for _, args := range [][]string{nil, {"--port", "9050"}, {"--data-dir", "/unused"}, {"--data-dir", "/unused", "--version"}} {
		if handled, code := runCLISubcommandArgs(args, "/unused"); handled || code != 0 {
			t.Fatalf("args=%v: handled=%v, code=%d", args, handled, code)
		}
	}
}
