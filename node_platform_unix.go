//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Node files are protected by their 0600 mode on Unix; passwords are stored
// as is.
func protectNodePassword(password string) (string, error) {
	return password, nil
}

func unprotectNodePassword(password string) (string, error) {
	return password, nil
}

func lockNodeStorage(path string) (func() error, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() error {
		return errors.Join(unix.Flock(int(file.Fd()), unix.LOCK_UN), file.Close())
	}, nil
}
