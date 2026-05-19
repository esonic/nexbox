package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rollingLog is an io.WriteCloser that writes to a rolling set of log files.
// When the active file exceeds maxBytes it is rotated: existing backups are
// shifted one position (nexbox.log → nexbox.log.1 → nexbox.log.2 …) and a
// fresh file is opened.  Files beyond maxFiles are deleted so that total disk
// usage stays bounded to roughly maxFiles × maxBytes.
type rollingLog struct {
	dir      string
	base     string // e.g. "nexbox.log"
	maxBytes int64
	maxFiles int
	mu       sync.Mutex
	f        *os.File
	size     int64
}

// newRollingLog creates the log directory if needed and opens the current log file.
func newRollingLog(dir, base string, maxBytes int64, maxFiles int) (*rollingLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	rl := &rollingLog{dir: dir, base: base, maxBytes: maxBytes, maxFiles: maxFiles}
	return rl, rl.openCurrent()
}

func (rl *rollingLog) openCurrent() error {
	path := filepath.Join(rl.dir, rl.base)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	rl.f, rl.size = f, info.Size()
	return nil
}

// rotate closes the current file, shifts all backups up by one, removes any
// file that would exceed maxFiles, then opens a fresh current file.
func (rl *rollingLog) rotate() error {
	rl.f.Close()
	rl.f = nil

	// Delete the backup that would be pushed past the limit.
	// With maxFiles=5: keep .1 … .4 plus current, so delete .4 before shifting.
	os.Remove(filepath.Join(rl.dir, fmt.Sprintf("%s.%d", rl.base, rl.maxFiles-1)))

	// Shift .{N-2} → .{N-1}, …, .1 → .2
	for i := rl.maxFiles - 2; i >= 1; i-- {
		src := filepath.Join(rl.dir, fmt.Sprintf("%s.%d", rl.base, i))
		dst := filepath.Join(rl.dir, fmt.Sprintf("%s.%d", rl.base, i+1))
		os.Rename(src, dst)
	}

	// current → .1
	os.Rename(
		filepath.Join(rl.dir, rl.base),
		filepath.Join(rl.dir, rl.base+".1"),
	)

	return rl.openCurrent()
}

func (rl *rollingLog) Write(p []byte) (n int, err error) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.size+int64(len(p)) > rl.maxBytes {
		if err := rl.rotate(); err != nil {
			// Rotation failed – fall back to stderr so logs are not silently lost.
			return os.Stderr.Write(p)
		}
	}

	n, err = rl.f.Write(p)
	rl.size += int64(n)
	return n, err
}

func (rl *rollingLog) Close() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.f != nil {
		return rl.f.Close()
	}
	return nil
}
