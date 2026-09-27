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
	// A backend may return core/tool.Failure only when it can establish a
	// complete unsuccessful outcome; that failure owns its model-visible output.
	// Other errors retain uncertainty, with observed Output carried by the Tool
	// as core/tool.CallError evidence. They are not automatic retry instructions.
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

// Output retains the executor's observations. A non-zero ExitCode is not an
// error. When Run returns an error, an unobserved exit code must not be reported
// as zero; ExitCode is -1 when no exit status was obtained. Available output
// remains evidence and does not establish a complete command result.
type Output struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
	// Truncation is an observation, independent of markers in command output.
	// A false value does not prove complete collection when Run returns an error.
	StdoutTruncated bool
	StderrTruncated bool

	// CancellationObserved reports that cancellation or timeout was observed
	// before returning; it does not establish why the process exited.
	CancellationObserved bool
}
