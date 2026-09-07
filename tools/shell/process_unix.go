//go:build unix

package shell

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

func configureProcessGroup(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Cancellation and final cleanup share one result. Signaling an already
	// terminated group can fail while its zombies are still awaiting reaping.
	command.Cancel = sync.OnceValue(func() error {
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
	})
	return nil
}
