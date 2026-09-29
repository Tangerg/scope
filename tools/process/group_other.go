//go:build !unix

package process

import "os/exec"

func configureGroup(*exec.Cmd) error {
	return ErrUnsupportedPlatform
}
