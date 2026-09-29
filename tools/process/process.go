// Package process owns a local command's start, wait, and process-group cleanup.
// It supports Unix hosts. Process groups are lifetime boundaries, not sandboxes:
// a descendant that creates another session can escape the group.
package process

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const defaultWaitDelay = time.Second

var (
	ErrInvalidConfig       = errors.New("process: invalid configuration")
	ErrNotStarted          = errors.New("process: not started")
	ErrStarted             = errors.New("process: already started")
	ErrClosed              = errors.New("process: closed")
	ErrUnsupportedPlatform = errors.New("process: process-group cleanup requires Unix")
)

// Config is frozen by New, except for the supplied I/O objects. Argv includes
// the executable as its first element. Directory must be explicit; it controls
// relative paths without confining the command. A nil Env snapshots the host
// environment; an empty non-nil Env starts with no inherited variables.
//
// I/O objects remain caller-owned. As with exec.Cmd, non-file streams use copy
// goroutines. Their Read and Write methods must return when their own lifetime
// ends: WaitDelay can close command pipes, but cannot interrupt arbitrary Go
// code blocked inside those methods. Callers must close their own streams to
// unblock such operations before waiting for Close.
type Config struct {
	Argv      []string
	Directory string
	Env       []string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	// WaitDelay bounds inherited command pipes after exit or cancellation.
	// Zero selects one second. A limit expiry is reported as exec.ErrWaitDelay
	// when exec.Cmd cannot otherwise report the command's failure.
	WaitDelay time.Duration
}

// Result records the terminal observations shared by every Wait caller. A
// non-zero exit code is an outcome, not an error; -1 means that no numeric exit
// status was observed. CancellationObserved does not establish the exit cause.
// exec.Cmd's collection-error precedence applies: a non-zero exit can mask a
// simultaneous stream-copy error, and only the first copy error is available.
type Result struct {
	ExitCode             int
	CancellationObserved bool
}

type state uint8

const (
	ready state = iota
	running
	finished
	closed
)

// Process has one execution lifetime, supplied to New. Construct it with New;
// the zero value is not usable. Start, Wait, and Close may run concurrently.
// Only its internal worker reaps the command; Wait observes that worker's
// immutable result, and Close joins it after requesting termination.
type Process struct {
	mu       sync.Mutex
	command  *exec.Cmd
	lifetime context.Context
	cancel   context.CancelFunc
	state    state
	done     chan struct{}
	result   Result
	err      error
}

func New(ctx context.Context, config Config) (*Process, error) {
	if len(config.Argv) == 0 || config.Argv[0] == "" {
		return nil, fmt.Errorf("%w: executable must not be empty", ErrInvalidConfig)
	}
	if config.Directory == "" {
		return nil, fmt.Errorf("%w: directory must not be empty", ErrInvalidConfig)
	}
	if config.WaitDelay < 0 {
		return nil, fmt.Errorf("%w: wait delay must not be negative", ErrInvalidConfig)
	}
	directory, err := filepath.Abs(config.Directory)
	if err != nil {
		return nil, fmt.Errorf("process.New: resolve directory %q: %w", config.Directory, err)
	}
	argv := slices.Clone(config.Argv)
	lifetime, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(lifetime, argv[0], argv[1:]...)
	command.Dir = directory
	command.Env = slices.Clone(config.Env)
	if config.Env == nil {
		command.Env = os.Environ()
	}
	command.Stdin = config.Stdin
	command.Stdout = config.Stdout
	command.Stderr = config.Stderr
	command.WaitDelay = cmp.Or(config.WaitDelay, defaultWaitDelay)
	if err := configureGroup(command); err != nil {
		cancel()
		return nil, err
	}
	return &Process{
		command:  command,
		lifetime: ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		result:   Result{ExitCode: -1},
	}, nil
}

// Start attempts creation once. A failed attempt is terminal and is also
// returned by Wait and Close; no retry can create a second command lifetime.
func (p *Process) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == finished || p.state == closed {
		return ErrClosed
	}
	if p.state == running {
		return ErrStarted
	}
	if err := p.command.Start(); err != nil {
		p.complete(finished, err)
		return err
	}
	p.state = running
	go p.reap()
	return nil
}

// Wait returns ErrNotStarted before a start attempt. After Start or Close, it
// joins the same completion and preserves the errors reported by exec.Cmd,
// except that exit status belongs to Result. Group-cleanup failures are joined
// to that error. Waiting never transfers lifecycle ownership.
func (p *Process) Wait() (Result, error) {
	p.mu.Lock()
	if p.state == ready {
		p.mu.Unlock()
		return Result{ExitCode: -1}, ErrNotStarted
	}
	p.mu.Unlock()
	<-p.done
	return p.result, p.err
}

// Close is idempotent, requests immediate group termination, and waits for
// cleanup. Before Start it prevents creation, returns nil, and makes Wait
// return ErrClosed. Otherwise it returns the same terminal error as Wait.
func (p *Process) Close() error {
	p.mu.Lock()
	if p.state == ready {
		p.complete(closed, ErrClosed)
		p.mu.Unlock()
		return nil
	}
	if p.state == running {
		p.cancel()
	}
	p.mu.Unlock()
	<-p.done
	if p.state == closed {
		return nil
	}
	return p.err
}

func (p *Process) reap() {
	err := p.command.Wait()
	cleanupErr := p.command.Cancel()
	if _, exited := errors.AsType[*exec.ExitError](err); exited {
		err = nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.command.ProcessState != nil {
		p.result.ExitCode = p.command.ProcessState.ExitCode()
	}
	p.complete(finished, errors.Join(err, cleanupErr))
}

// complete runs under mu; closing done publishes the immutable result and error.
func (p *Process) complete(terminal state, err error) {
	p.cancel()
	p.state = terminal
	p.result.CancellationObserved = p.lifetime.Err() != nil
	p.err = err
	close(p.done)
}
