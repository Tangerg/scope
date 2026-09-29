package process_test

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/Tangerg/scope/tools/process"
)

func TestEscapedDescendantCannotHoldCommandPipesOpen(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output := &lineWriter{ctx: ctx, lines: make(chan string, 1)}
	command := newCommand(t, ctx, process.Config{
		Argv:      []string{"/bin/sh", "-c", `setsid /bin/sh -c 'printf "%s\n" $$; exec sleep 60' &`},
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
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("escaped process unexpectedly confined: %v", err)
	}
}
