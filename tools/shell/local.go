package shell

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultShell             = "/bin/sh"
	shellCommandFlag         = "-c"
	defaultMaxBytesPerStream = 30 * 1024
	pipeCloseDelay           = time.Second
)

// LocalConfig makes the local process authority visible at construction. The
// directory controls relative-path resolution but is not a filesystem jail;
// callers that need confinement must supply an OS sandbox or container.
type LocalConfig struct {
	Directory string
	Shell     string
	// MaxBytesPerStream caps captured stdout and stderr independently.
	// Zero selects 30 KiB per stream; truncation markers are additional bytes.
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
func NewLocalExecutor(config LocalConfig) (*LocalExecutor, error) {
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
	if strings.TrimSpace(in.Cmd) == "" {
		return Output{ExitCode: -1}, ErrEmptyCommand
	}
	if in.Timeout < 0 {
		return Output{ExitCode: -1}, fmt.Errorf("%w: timeout must not be negative", ErrInvalidInput)
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if in.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, in.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, l.shell, shellCommandFlag, in.Cmd)
	cmd.Dir = l.directory
	if err := configureProcessGroup(cmd); err != nil {
		return Output{ExitCode: -1}, err
	}
	// Escaped descendants cannot keep inherited pipes alive indefinitely.
	cmd.WaitDelay = pipeCloseDelay

	stdout := newBoundedBuffer(l.maxBytesPerStream)
	stderr := newBoundedBuffer(l.maxBytesPerStream)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()
	err := cmd.Run()
	cleanupErr := cmd.Cancel()
	duration := time.Since(start)

	out := Output{
		Stdout:               stdout.finalize(),
		Stderr:               stderr.finalize(),
		StdoutTruncated:      stdout.dropped != 0,
		StderrTruncated:      stderr.dropped != 0,
		ExitCode:             -1,
		Duration:             duration,
		CancellationObserved: runCtx.Err() != nil,
	}
	if cmd.ProcessState != nil {
		out.ExitCode = cmd.ProcessState.ExitCode()
	}

	if err != nil {
		_, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			return out, errors.Join(err, cleanupErr)
		}
	}
	return out, cleanupErr
}

// Writes report len(p), nil even when truncated: breaking the child's stdio
// pipe would introduce an unrelated execution failure. The command continues
// until it exits or its context ends.
type boundedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped int
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	avail := b.limit - b.buf.Len()
	if avail <= 0 {
		b.dropped += len(p)
		return len(p), nil
	}
	if len(p) <= avail {
		return b.buf.Write(p)
	}
	n, _ := b.buf.Write(p[:avail])
	b.dropped += len(p) - n
	return len(p), nil
}

func (b *boundedBuffer) finalize() []byte {
	if b.dropped == 0 {
		return b.buf.Bytes()
	}
	out := b.buf.Bytes()

	if i := bytes.LastIndexByte(out, '\n'); i > 0 {
		shift := len(out) - (i + 1)
		out = out[:i+1]
		b.dropped += shift
	}
	return fmt.Appendf(out, "... [%d bytes truncated] ...\n", b.dropped)
}
