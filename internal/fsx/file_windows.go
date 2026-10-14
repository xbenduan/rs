//go:build windows

package fsx

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// SyncDir is a no-op because Windows does not support opening directories for
// the fsync operation used by Unix.
func SyncDir(string) error {
	return nil
}

func lockFile(file *os.File, wait bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}

func unlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
}
