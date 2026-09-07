//go:build unix

package shell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCancellationTerminatesBackgroundChild(t *testing.T) {
	skipWithoutShell(t)
	directory := t.TempDir()
	executor := mustLocalExecutor(t, LocalConfig{Directory: directory})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		output Output
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		output, err := executor.Run(ctx, Input{Cmd: "sleep 60 </dev/null >/dev/null 2>&1 & echo $! > child.pid; wait"})
		finished <- result{output: output, err: err}
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var childPID int
	for childPID == 0 {
		data, err := os.ReadFile(filepath.Join(directory, "child.pid"))
		if err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatal("child did not start")
		case <-ticker.C:
		}
	}
	defer syscall.Kill(childPID, syscall.SIGKILL)
	cancel()
	select {
	case completed := <-finished:
		if completed.err != nil || !completed.output.Killed {
			t.Fatalf("Run() = %+v, error = %v", completed.output, completed.err)
		}
	case <-deadline.C:
		t.Fatal("Run did not return after cancellation")
	}
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		// Some hosts defer reaping orphaned children; a zombie is already dead.
		state, inspectErr := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(childPID)).Output()
		if inspectErr == nil && strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("background child survived cancellation")
		case <-ticker.C:
		}
	}
}
