//go:build unix

package process

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

func configureGroup(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The owner never cancels before a successful Start. Caching the signal
	// result also avoids resending it to a zombie group on Darwin.
	command.Cancel = sync.OnceValue(func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return &os.SyscallError{Syscall: "kill process group", Err: err}
		}
		return nil
	})
	return nil
}
