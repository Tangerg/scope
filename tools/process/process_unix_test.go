//go:build unix

package process_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Tangerg/scope/tools/process"
)

func TestProcessRejectsInvalidConfig(t *testing.T) {
	for _, config := range []process.Config{
		{Directory: "."},
		{Argv: []string{""}, Directory: "."},
		{Argv: []string{"/bin/sh"}},
		{Argv: []string{"/bin/sh"}, Directory: ".", WaitDelay: -1},
	} {
		if _, err := process.New(t.Context(), config); !errors.Is(err, process.ErrInvalidConfig) {
			t.Fatalf("New(%+v) error = %v, want ErrInvalidConfig", config, err)
		}
	}
}

func TestCloseBeforeStartPreventsCreation(t *testing.T) {
	directory := t.TempDir()
	command := newCommand(t, t.Context(), process.Config{
		Argv: []string{"/bin/sh", "-c", "touch started"}, Directory: directory,
	})
	if result, err := command.Wait(); result.ExitCode != -1 || !errors.Is(err, process.ErrNotStarted) {
		t.Fatalf("Wait before Start = %+v, %v", result, err)
	}
	for range 2 {
		if err := command.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := command.Start(); !errors.Is(err, process.ErrClosed) {
		t.Fatalf("Start after Close = %v, want ErrClosed", err)
	}
	if result, err := command.Wait(); result.ExitCode != -1 || !errors.Is(err, process.ErrClosed) {
		t.Fatalf("Wait after Close = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed process created side effect: %v", err)
	}
}

func TestStartFailureIsTerminalAndObservable(t *testing.T) {
	command := newCommand(t, t.Context(), process.Config{
		Argv: []string{filepath.Join(t.TempDir(), "missing")}, Directory: ".",
	})
	startErr := command.Start()
	if !errors.Is(startErr, os.ErrNotExist) {
		t.Fatalf("Start = %v, want os.ErrNotExist", startErr)
	}
	for range 2 {
		result, err := command.Wait()
		if result.ExitCode != -1 || !errors.Is(err, startErr) {
			t.Fatalf("Wait = %+v, %v, want original start error", result, err)
		}
		if err := command.Close(); !errors.Is(err, startErr) {
			t.Fatalf("Close = %v, want original start error", err)
		}
	}
	if err := command.Start(); !errors.Is(err, process.ErrClosed) {
		t.Fatalf("second Start = %v, want ErrClosed", err)
	}
}

func TestCancelledBeforeStartDoesNotCreateProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	command := newCommand(t, ctx, process.Config{Argv: []string{"/bin/sh", "-c", "exit 0"}, Directory: "."})
	if err := command.Start(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want context.Canceled", err)
	}
	result, err := command.Wait()
	if result.ExitCode != -1 || !result.CancellationObserved || !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %+v, %v", result, err)
	}
}

func TestNaturalExitFreezesObservations(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := newCommand(t, ctx, process.Config{
		Argv:      []string{"/bin/sh", "-c", "printf output; printf error >&2; exit 7"},
		Directory: ".", Stdout: &stdout, Stderr: &stderr,
	})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	want := process.Result{ExitCode: 7}
	if result, err := command.Wait(); result != want || err != nil {
		t.Fatalf("Wait = %+v, %v, want %+v, nil", result, err, want)
	}
	cancel()
	if result, err := command.Wait(); result != want || err != nil {
		t.Fatalf("repeated Wait = %+v, %v, want frozen result %+v", result, err, want)
	}
	if stdout.String() != "output" || stderr.String() != "error" {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
	if err := command.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFreezesArgumentAndEnvironmentSlices(t *testing.T) {
	argv := []string{"/bin/sh", "-c", `printf '%s' "$PROCESS_VALUE"`}
	env := []string{"PROCESS_VALUE=original"}
	var stdout bytes.Buffer
	command := newCommand(t, t.Context(), process.Config{Argv: argv, Env: env, Directory: ".", Stdout: &stdout})
	argv[2] = "exit 9"
	env[0] = "PROCESS_VALUE=changed"
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if result, err := command.Wait(); result.ExitCode != 0 || err != nil || stdout.String() != "original" {
		t.Fatalf("Wait = %+v, %v, stdout = %q", result, err, stdout.String())
	}
}

func TestConcurrentStartCloseAndWaitShareCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := newCommand(t, ctx, process.Config{
		Argv: []string{"/bin/sh", "-c", "sleep 60"}, Directory: ".",
	})
	const callers = 16
	startErrors := make(chan error, callers)
	var starts sync.WaitGroup
	for range callers {
		starts.Go(func() { startErrors <- command.Start() })
	}
	starts.Wait()
	close(startErrors)
	started := 0
	for err := range startErrors {
		switch {
		case err == nil:
			started++
		case errors.Is(err, process.ErrStarted):
		default:
			t.Fatalf("Start = %v", err)
		}
	}
	if started != 1 {
		t.Fatalf("successful starts = %d, want 1", started)
	}
	var observers sync.WaitGroup
	for range callers {
		observers.Go(func() {
			if err := command.Close(); err != nil {
				t.Errorf("Close = %v", err)
			}
		})
		observers.Go(func() {
			result, err := command.Wait()
			if result != (process.Result{ExitCode: -1}) || err != nil {
				t.Errorf("Wait = %+v, %v", result, err)
			}
		})
	}
	observers.Wait()
}

func TestCancellationTerminatesDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output := &lineWriter{lines: make(chan string, 1), ctx: ctx}
	command := newCommand(t, ctx, process.Config{
		Argv:      []string{"/bin/sh", "-c", "sleep 60 & printf '%s\n' $!; wait"},
		Directory: ".", Stdout: output,
	})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := readPID(t, output.lines, ctx)
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	result, err := command.Wait()
	if result.ExitCode != -1 || !result.CancellationObserved || err != nil {
		t.Fatalf("Wait = %+v, %v", result, err)
	}
	assertTerminated(t, pid)
}

func TestNaturalExitBoundsInheritedPipesAndCleansGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output := &lineWriter{lines: make(chan string, 1), ctx: ctx}
	command := newCommand(t, ctx, process.Config{
		Argv:      []string{"/bin/sh", "-c", "sleep 60 & printf '%s\n' $!"},
		Directory: ".", Stdout: output, WaitDelay: 20 * time.Millisecond,
	})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := readPID(t, output.lines, ctx)
	defer syscall.Kill(pid, syscall.SIGKILL)
	result, err := command.Wait()
	if result != (process.Result{ExitCode: 0}) || !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("Wait = %+v, %v, want exit 0 and exec.ErrWaitDelay", result, err)
	}
	if closeErr := command.Close(); !errors.Is(closeErr, err) {
		t.Fatalf("Close = %v, want retained error %v", closeErr, err)
	}
	assertTerminated(t, pid)
}

func TestOutputCollectionFailureRemainsObservable(t *testing.T) {
	failure := errors.New("output unavailable")
	command := newCommand(t, t.Context(), process.Config{
		Argv: []string{"/bin/sh", "-c", "printf output"}, Directory: ".", Stdout: &failedWriter{err: failure},
	})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if result, err := command.Wait(); result.ExitCode != 0 || !errors.Is(err, failure) {
		t.Fatalf("Wait = %+v, %v, want collection failure", result, err)
	}
}

func TestInputFailureRemainsObservable(t *testing.T) {
	failure := errors.New("input unavailable")
	command := newCommand(t, t.Context(), process.Config{
		Argv: []string{"/bin/sh", "-c", "cat >/dev/null"}, Directory: ".", Stdin: &failedReader{err: failure},
	})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if result, err := command.Wait(); result.ExitCode != 0 || !errors.Is(err, failure) {
		t.Fatalf("Wait = %+v, %v, want input failure", result, err)
	}
}

func newCommand(t *testing.T, ctx context.Context, config process.Config) *process.Process {
	t.Helper()
	command, err := process.New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return command
}

type lineWriter struct {
	ctx   context.Context
	lines chan string
	buf   bytes.Buffer
}

func (l *lineWriter) Write(data []byte) (int, error) {
	l.buf.Write(data)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			l.buf.WriteString(line)
			return len(data), nil
		}
		select {
		case l.lines <- strings.TrimSuffix(line, "\n"):
		case <-l.ctx.Done():
			return 0, l.ctx.Err()
		}
	}
}

type failedWriter struct{ err error }

func (f *failedWriter) Write([]byte) (int, error) { return 0, f.err }

type failedReader struct{ err error }

func (f *failedReader) Read([]byte) (int, error) { return 0, f.err }

func readPID(t *testing.T, lines <-chan string, ctx context.Context) int {
	t.Helper()
	select {
	case line := <-lines:
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			t.Fatalf("child pid %q: %v", line, err)
		}
		return pid
	case <-ctx.Done():
		t.Fatal("child did not become ready")
		return 0
	}
}

func assertTerminated(t *testing.T, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		// Orphan reaping depends on the host; a zombie no longer executes.
		output, _ := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if strings.HasPrefix(strings.TrimSpace(string(output)), "Z") {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("descendant %d survived cleanup", pid)
		case <-ticker.C:
		}
	}
}

func TestLongRunningStdioExchangesBeforeShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	input, requests, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer requests.Close()
	responses := &lineWriter{ctx: ctx, lines: make(chan string, 1)}
	command := newCommand(t, ctx, process.Config{
		Argv:      []string{"/bin/sh", "-c", `while IFS= read -r request; do printf '%s\n' "$request"; done`},
		Directory: ".", Stdin: input, Stdout: responses,
	})
	if startErr := command.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	defer command.Close()
	for _, request := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	} {
		if _, writeErr := fmt.Fprintln(requests, request); writeErr != nil {
			t.Fatal(writeErr)
		}
		select {
		case response := <-responses.lines:
			if response != request {
				t.Fatalf("response = %q, want %q", response, request)
			}
		case <-ctx.Done():
			t.Fatal("stdio response did not arrive while the command was running")
		}
	}
	if closeErr := requests.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	result, err := command.Wait()
	if result != (process.Result{ExitCode: 0}) || err != nil {
		t.Fatalf("Wait after stdin EOF = %+v, %v", result, err)
	}
}
