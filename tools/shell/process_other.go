//go:build !unix

package shell

import (
	"errors"
	"os/exec"
)

func configureProcessGroup(*exec.Cmd) error {
	return errors.New("shell: local execution requires Unix process-group cancellation")
}
