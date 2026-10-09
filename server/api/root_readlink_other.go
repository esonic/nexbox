//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package api

import (
	"errors"
	"os"
)

func readlinkAtRoot(_ *os.Root, _ string) (string, error) {
	return "", errors.New("repository-scoped symlink reading is not supported on this platform")
}
