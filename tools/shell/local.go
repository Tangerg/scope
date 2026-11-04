package shell

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tangerg/scope/tools/process"
)

const (
	defaultShell             = "/bin/sh"
	shellCommandFlag         = "-c"
	defaultMaxBytesPerStream = 30 * 1024
)

// LocalExecutorConfig makes the local process authority visible at construction. The
// directory controls relative-path resolution but is not a filesystem jail;
// callers that need confinement must supply an OS sandbox or container.
type LocalExecutorConfig struct {
	Directory string
	Shell     string
	// MaxBytesPerStream caps captured stdout and stderr independently.
	// Zero selects 30 KiB per stream. Capture never inserts presentation text.
	MaxBytesPerStream int
}

// LocalExecutor runs commands on the local host through one immutable
// construction-time configuration. Each call owns a Unix process group and
// kills that group on cancellation or return, including background children.
// A command that deliberately creates another session requires a host sandbox
// to keep its lifetime confined; this executor is not a process sandbox.
type LocalExecutor struct {
	directory         string
	shell             string
	maxBytesPerStream int
}

// NewLocalExecutor freezes an absolute working directory and output limits.
func NewLocalExecutor(config LocalExecutorConfig) (*LocalExecutor, error) {
	if config.Directory == "" {
		return nil, fmt.Errorf("%w: directory must not be empty", ErrInvalidConfig)
	}
	if strings.TrimSpace(config.Shell) == "" && config.Shell != "" {
		return nil, fmt.Errorf("%w: shell must not be blank", ErrInvalidConfig)
	}
	if config.MaxBytesPerStream < 0 {
		return nil, fmt.Errorf("%w: maximum output bytes must not be negative", ErrInvalidConfig)
	}
	directory, err := filepath.Abs(config.Directory)
	if err != nil {
		return nil, fmt.Errorf("shell.NewLocalExecutor: resolve directory %q: %w", config.Directory, err)
	}
	return &LocalExecutor{
		directory:         filepath.Clean(directory),
		shell:             cmp.Or(config.Shell, defaultShell),
		maxBytesPerStream: cmp.Or(config.MaxBytesPerStream, defaultMaxBytesPerStream),
	}, nil
}

func (l *LocalExecutor) Run(ctx context.Context, in Input) (Output, error) {
	if l == nil {
		return Output{ExitCode: -1}, ErrNilExecutor
	}
	if l.directory == "" || l.shell == "" || l.maxBytesPerStream <= 0 {
		return Output{ExitCode: -1}, ErrInvalidConfig
	}
	if err := in.Validate(); err != nil {
		return Output{ExitCode: -1}, err
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if in.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, in.Timeout)
		defer cancel()
	}

	stdout := newBoundedBuffer(l.maxBytesPerStream)
	stderr := newBoundedBuffer(l.maxBytesPerStream)
	command, err := process.New(runCtx, process.Config{
		Argv:      []string{l.shell, shellCommandFlag, in.Cmd},
		Directory: l.directory,
		Stdout:    stdout,
		Stderr:    stderr,
	})
	if err != nil {
		return Output{ExitCode: -1}, err
	}

	start := time.Now()
	if startErr := command.Start(); startErr != nil {
		return Output{ExitCode: -1, Duration: time.Since(start), CancellationObserved: runCtx.Err() != nil}, startErr
	}
	result, err := command.Wait()
	duration := time.Since(start)

	out := Output{
		Stdout:               stdout.buf.Bytes(),
		Stderr:               stderr.buf.Bytes(),
		StdoutDroppedBytes:   stdout.dropped,
		StderrDroppedBytes:   stderr.dropped,
		ExitCode:             result.ExitCode,
		Duration:             duration,
		CancellationObserved: result.CancellationObserved,
	}
	return out, err
}

// Writes report len(p), nil even when truncated: breaking the child's stdio
// pipe would introduce an unrelated execution failure. The command continues
// until it exits or its context ends.
type boundedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped uint64
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	avail := b.limit - b.buf.Len()
	if avail <= 0 {
		b.dropped += uint64(len(p))
		return len(p), nil
	}
	if len(p) <= avail {
		return b.buf.Write(p)
	}
	n, _ := b.buf.Write(p[:avail])
	b.dropped += uint64(len(p) - n)
	return len(p), nil
}
