//go:build !windows

package server

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(process *os.Process) error {
	return syscall.Kill(-process.Pid, syscall.SIGKILL)
}
