//go:build !windows

package fsx

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// SyncDir durably flushes directory metadata where the platform supports it.
func SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func lockFile(file *os.File, wait bool) error {
	how := unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}
	err := unix.Flock(int(file.Fd()), how)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
