//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package api

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// readlinkAtRoot opens the containing directory through os.Root and then reads
// the final symlink relative to that stable directory descriptor. This keeps
// the operation inside the authorized repository even if an ancestor path is
// concurrently renamed or replaced.
func readlinkAtRoot(root *os.Root, name string) (string, error) {
	directory, err := root.Open(filepath.Dir(name))
	if err != nil {
		return "", err
	}
	defer directory.Close()

	buffer := make([]byte, 256)
	for len(buffer) <= 1<<20 {
		count, readErr := unix.Readlinkat(int(directory.Fd()), filepath.Base(name), buffer)
		if readErr != nil {
			return "", readErr
		}
		if count < len(buffer) {
			return string(buffer[:count]), nil
		}
		buffer = make([]byte, len(buffer)*2)
	}
	return "", fmt.Errorf("symlink target is too long")
}
