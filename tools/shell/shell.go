package shell

import (
	"context"
	"time"
)

// Executor is the authority boundary behind the model-facing shell tool. The
// concrete implementation owns process creation, working-directory policy,
// environment exposure, output capture, termination, and platform semantics.
type Executor interface {
	// Run executes exactly one command within the executor's frozen authority.
	// It honors ctx and Input.Timeout, returns non-zero exit status as Output
	// rather than error, and reserves error for spawn, I/O, or collection failure.
	// On error, Output retains all available execution facts. Neither an error
	// nor missing output proves that the command had no side effects.
	Run(ctx context.Context, in Input) (Output, error)
}

// Input captures everything an executor needs to launch a single
// command. Only Cmd is required.
type Input struct {
	// Cmd is the shell command line. Required.
	Cmd string

	// Timeout bounds the run. 0 = no timeout; ctx cancellation still
	// applies.
	Timeout time.Duration
}

// Output is what every executor returns. A non-zero ExitCode is
// not an error — only spawn/I/O failures populate the error return.
type Output struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration

	// CancellationObserved reports that cancellation or timeout was observed
	// before returning; it does not establish why the process exited.
	CancellationObserved bool
}
