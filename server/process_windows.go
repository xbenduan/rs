//go:build windows

package server

import (
	"os"
	"os/exec"
)

func configureProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(process *os.Process) error {
	return process.Kill()
}
