//go:build unix

package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestGrepPreservesCancellationDuringAnIncompleteEvent(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	readyPath := filepath.Join(bin, "ready")
	holdPath := filepath.Join(bin, "hold")
	ready := openGrepBarrier(t, readyPath)
	_ = openGrepBarrier(t, holdPath)
	marker := filepath.Join(bin, "validated")
	script := fmt.Sprintf(`#!/bin/sh
if [ ! -f %q ]; then
  : > %q
  exit 0
fi
printf '%%s' '{"type":"match","data":'
printf '1' > %q
read -r ignored < %q
`, marker, marker, readyPath, holdPath)
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
	var signal [1]byte
	if _, err := io.ReadFull(ready, signal[:]); err != nil {
		t.Fatal(err)
	}
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
	holdPath := filepath.Join(bin, "hold")
	_ = openGrepBarrier(t, holdPath)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '{\"type\":\"summary\"}'\nread -r ignored < %q\n", holdPath)
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

func openGrepBarrier(t *testing.T, path string) *os.File {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}
