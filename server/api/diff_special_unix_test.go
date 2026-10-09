//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFileDiffDoesNotBlockOnNamedPipe(t *testing.T) {
	repository := t.TempDir()
	initAPIRepo(t, repository)
	pipePath := filepath.Join(repository, "events.pipe")
	if err := unix.Mkfifo(pipePath, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	diff, err := getFileDiff(ctx, repository, "events.pipe", false, false)
	if err != nil {
		t.Fatalf("named-pipe diff: %v", err)
	}
	if diff != "" {
		t.Fatalf("named-pipe diff = %q, want empty", diff)
	}
	if ctx.Err() != nil || time.Since(started) >= time.Second {
		t.Fatalf("named-pipe diff blocked: duration=%s context=%v", time.Since(started), ctx.Err())
	}
}
