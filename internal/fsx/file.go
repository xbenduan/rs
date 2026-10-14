// Package fsx provides shared durable file operations.
package fsx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrLocked reports that a non-blocking file lock is already held.
var ErrLocked = errors.New("file is locked")

// WriteFileAtomic writes data through a synced temporary file in the same
// directory. Missing parent directories are created with mode 0700.
func WriteFileAtomic(path string, data []byte, mode os.FileMode, replace bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".rs-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := commitTempFile(tmpName, path, replace); err != nil {
		return err
	}
	return SyncDir(dir)
}

// LockFile holds an exclusive lock, waiting until it becomes available.
func LockFile(path string) (func() error, error) {
	return acquireLock(path, true)
}

// TryLockFile acquires an exclusive lock without waiting.
func TryLockFile(path string) (func() error, error) {
	return acquireLock(path, false)
}

func commitTempFile(tmpName, path string, replace bool) error {
	if replace {
		return os.Rename(tmpName, path)
	}
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("refusing to overwrite existing file: %s", path)
		}
		return err
	}
	return os.Remove(tmpName)
}

func acquireLock(path string, wait bool) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file, wait); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() error {
		return errors.Join(unlockFile(file), file.Close())
	}, nil
}
