package run

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestAwaitCancellationAtTheLocalResultBoundary(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "completed"
		if canceled {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				log := integrityInitialLog(t)
				state := controlValue(projectLog(log))
				tree := newTreeRuntime(nil, state, t.Context())
				defer tree.cancel()
				process := state.processes[log.Root]
				handle := &Handle[string]{tree: tree, process: process, id: log.Root, epoch: state.epoch}
				terminal := Termination{output: process.started.Input}
				var decision Record = Stepped{Process: log.Root, Step: 1, Transition: controlValue(Complete(0, terminal.output))}
				if canceled {
					terminal = Termination{cause: TerminationCauseHostCancellation, reason: "host canceled"}
					decision = ControlRequested{Process: log.Root, Origin: ControlOriginHost, Kind: ControlCancel, Reason: terminal.reason, At: process.started.At}
				}
				finished := integrityAppend(log, decision, Exited{Process: log.Root, Termination: terminal, At: process.started.At})
				if err := finished.Validate(); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var result Result[string]
				var awaitErr error
				returned := make(chan struct{})
				go func() {
					result, awaitErr = handle.Await(ctx)
					close(returned)
				}()
				synctest.Wait()

				// Publication selects the terminal wake-up before cancellation,
				// while the writer lock keeps the result snapshot unreadable.
				tree.mu.Lock()
				var commitErr error
				for _, entry := range finished.Entries[len(log.Entries):] {
					if commitErr = state.apply(entry); commitErr != nil {
						break
					}
				}
				cancel()
				tree.mu.Unlock()
				<-returned
				if commitErr != nil {
					t.Fatal(commitErr)
				}
				if result.Valid() || !errors.Is(awaitErr, context.Canceled) {
					t.Fatalf("canceled observer received terminal evidence: valid=%t error=%v", result.Valid(), awaitErr)
				}
				observed := controlValue(handle.Await(t.Context()))
				if !observed.Valid() || !observed.Termination().equal(terminal) {
					t.Fatalf("observer cancellation changed the committed outcome: %+v", observed)
				}
			})
		})
	}
}

func TestAwaitCancellationAtTheCommittedResultBoundary(t *testing.T) {
	storeFailure := errors.New("terminal observation failed")
	for _, test := range []struct {
		name             string
		cancelBeforeRead bool
		loadError        error
		wantError        error
		wantReads        int
	}{
		{name: "before read", cancelBeforeRead: true, wantError: context.Canceled},
		{name: "after read", wantError: context.Canceled, wantReads: 1},
		{name: "read failure", loadError: storeFailure, wantError: storeFailure, wantReads: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := publicBoundaryLog(t, true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reads := 0
			store := &publicObservationStore{load: func(context.Context, ProcessID) (Log, bool, error) {
				reads++
				cancel()
				if test.loadError != nil {
					return Log{}, false, test.loadError
				}
				return log, true, nil
			}}
			runtime := kernelRuntime(t, Config{Store: store})
			handle := &Handle[string]{runtime: runtime, id: log.Root}
			if test.cancelBeforeRead {
				cancel()
			}
			result, err := handle.Await(ctx)
			if result.Valid() || !errors.Is(err, test.wantError) || reads != test.wantReads {
				t.Fatalf("canceled observation crossed its boundary: valid=%t error=%v reads=%d; want error=%v reads=%d", result.Valid(), err, reads, test.wantError, test.wantReads)
			}
		})
	}
}
