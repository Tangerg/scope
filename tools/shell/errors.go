package shell

import "errors"

var (
	// ErrEmptyCommand and ErrInvalidInput reject the command before execution.
	// Backends must not return either after dispatch or mutation.
	ErrEmptyCommand  = errors.New("shell: command must not be empty")
	ErrInvalidConfig = errors.New("shell: executor configuration is invalid")
	ErrInvalidInput  = errors.New("shell: input is invalid")
	ErrNilExecutor   = errors.New("shell: executor must not be nil")
)
