//go:build unix

package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const grepBarrierAddressEnv = "SCOPE_GREP_BARRIER_ADDRESS"

func TestGrepPreservesCancellationDuringAnIncompleteEvent(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	ready, barrierCommand := newGrepBarrier(t)
	marker := filepath.Join(bin, "validated")
	script := fmt.Sprintf(`#!/bin/sh
if [ ! -f %q ]; then
  : > %q
  exit 0
fi
printf '%%s' '{"type":"match","data":'
%s
`, marker, marker, barrierCommand)
	if err := os.WriteFile(filepath.Join(bin, "rg"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	lifetime, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	stopClosing := context.AfterFunc(lifetime, func() { _ = ready.Close() })
	defer stopClosing()
	ctx, cancel := context.WithCancelCause(lifetime)
	defer cancel(nil)
	cause := errors.New("host stopped content search")
	executor := mustLocalExecutor(t, directory)
	finished := make(chan error, 1)
	go func() {
		_, err := executor.Grep(ctx, GrepRequest{Path: "source.txt", Pattern: "needle"})
		finished <- err
	}()
	connection, err := ready.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	cancel(cause)
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("Grep lost cancellation while decoding an interrupted event: %v", err)
		}
	case <-lifetime.Done():
		t.Fatal("canceled grep did not finish")
	}
}

func TestRunRipgrepPreservesCancellationAtOutputBudget(t *testing.T) {
	bin := t.TempDir()
	_, barrierCommand := newGrepBarrier(t)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '{\"type\":\"summary\"}'\n%s\n", barrierCommand)
	executable := filepath.Join(bin, "rg")
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	lifetime, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancelCause(lifetime)
	defer cancel(nil)
	cause := errors.New("host stopped truncated content search")
	input := &cancelOnClosedGrepInput{cancel: cancel, cause: cause}
	decoder := newRipgrepDecoder(GrepOutputContent, 1, 1)
	err := runRipgrep(ctx, executable, nil, input, decoder)
	if !decoder.response.Truncated {
		t.Fatal("ripgrep did not reach the output budget")
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("output budget swallowed concurrent cancellation: %v", err)
	}
}

// Wait must finish copying stdin after budget cleanup kills the child. That
// pipe closure places cancellation before the budget branch returns.
type cancelOnClosedGrepInput struct {
	cancel context.CancelCauseFunc
	cause  error
}

func (c *cancelOnClosedGrepInput) Read(data []byte) (int, error) {
	clear(data)
	return len(data), nil
}

func (c *cancelOnClosedGrepInput) WriteTo(writer io.Writer) (int64, error) {
	var written int64
	var buffer [4096]byte
	for {
		n, err := writer.Write(buffer[:])
		written += int64(n)
		if err != nil {
			c.cancel(c.cause)
			return written, err
		}
	}
}

func newGrepBarrier(t *testing.T) (net.Listener, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	t.Setenv(grepBarrierAddressEnv, listener.Addr().String())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return listener, fmt.Sprintf("exec %q -test.run=^TestGrepBarrierProcess$", executable)
}

// The child connects only after writing its partial event. Socket closure
// releases its wait on every supported Unix platform, including Darwin where
// nonblocking FIFOs do not participate in Go's file poller.
func TestGrepBarrierProcess(t *testing.T) {
	address := os.Getenv(grepBarrierAddressEnv)
	if address == "" {
		return
	}
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err = io.Copy(io.Discard, connection); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
