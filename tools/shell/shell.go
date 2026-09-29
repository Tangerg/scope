package shell

import (
	"context"
	"time"
)

// Executor is the authority boundary behind the model-facing shell tool. The
// concrete implementation owns process creation, working-directory policy,
// environment exposure, output capture, termination, and platform semantics.
type Executor interface {
	// Run executes one command, honoring ctx and Input.Timeout. A non-zero exit
	// status is Output, not an error. An error reports spawn, I/O, or
	// collection failure and leaves side effects uncertain; Output still
	// carries every available observation. Return core/tool.Failure only for an
	// established unsuccessful outcome, since it owns its model-visible output.
	Run(ctx context.Context, in Input) (Output, error)
}

type Input struct {
	Cmd string

	// Timeout zero means no timeout beyond ctx.
	Timeout time.Duration
}

// Output retains the executor's observations. ExitCode is -1 when no exit
// status was obtained, so an unobserved exit is never reported as success.
type Output struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
	// Truncation is observed by the executor, independent of markers in the
	// output. False does not prove complete collection when Run returns an error.
	StdoutTruncated bool
	StderrTruncated bool

	// CancellationObserved reports that cancellation or timeout was observed
	// before returning; it does not establish why the process exited.
	CancellationObserved bool
}
