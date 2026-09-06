//go:build unix

package shell

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureProcessGroup(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return terminateProcessGroup(command)
	}
	return nil
}

func terminateProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return &os.SyscallError{Syscall: "kill process group", Err: err}
	}
	return nil
}
