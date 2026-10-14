package server

import (
	"errors"
	"fmt"

	"atlas/rs/internal/fsx"
)

// LockState holds the lock that keeps rs serve, init and credential reset
// from writing the same auth state concurrently. It fails immediately when
// another process, normally a running rs serve, holds it.
func LockState(stateFile string) (func() error, error) {
	unlock, err := fsx.TryLockFile(stateFile + ".lock")
	if errors.Is(err, fsx.ErrLocked) {
		return nil, fmt.Errorf("auth state %s is in use; stop rs serve first", stateFile)
	}
	return unlock, err
}
