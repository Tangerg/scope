package shell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessGroupCancellationKeepsItsCompletedResult(t *testing.T) {
	command := exec.CommandContext(t.Context(), "/bin/sh", "-c", "sleep 60")
	if err := configureProcessGroup(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		if err := command.Wait(); err != nil {
			if _, exited := errors.AsType[*exec.ExitError](err); !exited {
				t.Error(err)
			}
		}
	}()
	if err := command.Cancel(); err != nil {
		t.Fatal(err)
	}

	// Keep the terminated child unreaped so a second group signal reaches only
	// a zombie. Darwin rejects that signal even though termination succeeded.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", strconv.Itoa(command.Process.Pid)).Output()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("terminated child did not reach zombie state")
		case <-ticker.C:
		}
	}
	if err := command.Cancel(); err != nil {
		t.Fatalf("completed group cancellation changed result: %v", err)
	}
}
