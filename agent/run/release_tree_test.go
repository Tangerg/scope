package run

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestReleaseTreeJoinsTheExitedWriterWithoutRetainingTheRuntimeLock(t *testing.T) {
	for _, test := range []struct {
		name    string
		cancel  bool
		replace bool
		want    error
	}{
		{name: "join"},
		{name: "cancellation", cancel: true, want: context.Canceled},
		{name: "replacement", replace: true, want: ErrTreeNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				log := integrityInitialLog(t)
				started := log.Entries[0].Record.(Started)
				terminal := Termination{output: started.Input}
				log = integrityAppend(log,
					Stepped{Process: log.Root, Step: 1, Transition: controlValue(Complete(0, terminal.output))},
					Exited{Process: log.Root, Termination: terminal, At: started.At})
				state := controlValue(projectLog(log))
				tree := newTreeRuntime(nil, state, t.Context())
				defer tree.cancel()
				runtime := &Runtime{roots: map[ProcessID]*treeRuntime{log.Root: tree}}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				returned := make(chan error, 1)
				go func() { returned <- runtime.ReleaseTree(ctx, log.Root) }()
				synctest.Wait()
				select {
				case err := <-returned:
					t.Fatalf("release returned before the exited writer stopped: %v", err)
				default:
				}
				if _, present := runtime.Process(log.Root); !present {
					t.Fatal("release dropped ownership before the writer stopped")
				}
				if test.replace {
					replacement := newTreeRuntime(nil, state, t.Context())
					defer replacement.cancel()
					runtime.mu.Lock()
					runtime.roots[log.Root] = replacement
					runtime.mu.Unlock()
				}
				if test.cancel {
					cancel()
				} else {
					close(tree.done)
				}
				if err := <-returned; !errors.Is(err, test.want) {
					t.Fatalf("release error=%v, want %v", err, test.want)
				}
				_, present := runtime.Process(log.Root)
				if present != (test.cancel || test.replace) {
					t.Fatalf("release changed the wrong ownership: present=%t", present)
				}
			})
		})
	}
}
