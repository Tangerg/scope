package run

import (
	"context"
	"errors"
	"iter"
	"testing"
	"testing/synctest"
)

func TestTerminalRecoveryObservationsStopAtCommittedHead(t *testing.T) {
	for _, test := range []struct {
		name     string
		interval uint64
	}{
		{"replayed prefix", 0},
		{"checkpoint prefix", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			original := kernelRuntime(t, Config{Store: store, CheckpointInterval: test.interval})
			program := treeFixtureProgram(t, original, 0, false, false, Budget{})
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			handle := controlValue(original.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
			if _, err := handle.Await(ctx); err != nil {
				t.Fatal(err)
			}
			if err := original.Close(ctx); err != nil {
				t.Fatal(err)
			}
			exited := controlValue(handle.Inspect(ctx)).Head
			replacement := kernelRuntime(t, Config{Store: store})
			recovered := controlValue(replacement.Recover(ctx, program, handle.ID()))
			head := controlValue(recovered.Inspect(ctx)).Head
			if head != exited+1 || recovered.Epoch() != handle.Epoch()+1 {
				t.Fatal("recovery did not commit a new writer epoch")
			}
			for _, after := range []LSN{0, exited, head} {
				cursor := after
				for entry, err := range recovered.Observe(ctx, after) {
					if err != nil || entry.LSN != cursor+1 {
						t.Fatalf("observation after %d: entry=%+v error=%v", after, entry, err)
					}
					cursor = entry.LSN
				}
				if cursor != head {
					t.Fatalf("observation after %d stopped at %d, want %d", after, cursor, head)
				}
			}
		})
	}
}

func TestTerminalObservationFencesRecoveryAtFirstPull(t *testing.T) {
	for _, test := range []struct {
		name       string
		beforePull bool
		beforeRead bool
	}{
		{"recovery before first pull", true, false},
		{"recovery before history snapshot", false, true},
		{"recovery during delivery", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			memory := NewMemoryStore()
			store := &publicObservationStore{store: memory}
			original := kernelRuntime(t, Config{Store: store})
			program := treeFixtureProgram(t, original, 0, false, false, Budget{})
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			handle := controlValue(original.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
			if _, err := handle.Await(ctx); err != nil {
				t.Fatal(err)
			}
			if err := original.Close(ctx); err != nil {
				t.Fatal(err)
			}
			head := controlValue(handle.Inspect(ctx)).Head
			replacement := kernelRuntime(t, Config{Store: store})
			var recoveredHead LSN
			recoverOnce := func() {
				if recoveredHead == 0 {
					recovered := controlValue(replacement.Recover(ctx, program, handle.ID()))
					recoveredHead = controlValue(recovered.Inspect(ctx)).Head
				}
			}
			observations := handle.Observe(ctx, 0)
			if test.beforePull {
				recoverOnce()
				head = recoveredHead
			}
			if test.beforeRead {
				store.read = func(readContext context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error] {
					recoverOnce()
					return memory.Read(readContext, root, after)
				}
			}
			var cursor LSN
			for entry, err := range observations {
				if err != nil || entry.LSN != cursor+1 {
					t.Fatalf("observation entry=%+v error=%v", entry, err)
				}
				cursor = entry.LSN
				recoverOnce()
			}
			if recoveredHead == 0 || cursor != head {
				t.Fatalf("observation ended at %d, want fixed head %d; recovered head=%d", cursor, head, recoveredHead)
			}
		})
	}
}

func TestActiveObservationDeliversTheWholeTerminalSnapshot(t *testing.T) {
	memory := NewMemoryStore()
	store := &publicObservationStore{store: memory}
	original := kernelRuntime(t, Config{Store: store})
	program := lifecycleEchoProgram(t, original, "test.observation")
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	handle := controlValue(original.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
	waitForKernelStep(t, ctx, handle, 1)
	replacement := kernelRuntime(t, Config{Store: store})
	var head LSN
	store.read = func(readContext context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error] {
		if head == 0 {
			if err := handle.Answer(ctx, kernelWait().Key, 42); err != nil {
				t.Fatal(err)
			}
			if _, err := handle.Await(ctx); err != nil {
				t.Fatal(err)
			}
			if err := original.Close(ctx); err != nil {
				t.Fatal(err)
			}
			recovered := controlValue(replacement.Recover(ctx, program, handle.ID()))
			head = controlValue(recovered.Inspect(ctx)).Head
		}
		return memory.Read(readContext, root, after)
	}
	var cursor LSN
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil || entry.LSN != cursor+1 {
			t.Fatalf("active observation entry=%+v error=%v", entry, err)
		}
		cursor = entry.LSN
	}
	if cursor != head {
		t.Fatalf("terminal snapshot ended at %d, want %d", cursor, head)
	}
}

func TestTerminalObservationRejectsHistoryShorterThanItsConfirmedHead(t *testing.T) {
	store := &publicObservationStore{store: NewMemoryStore()}
	runtime := kernelRuntime(t, Config{Store: store})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
	if _, err := handle.Await(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	log := controlValue(runtime.History(ctx, handle.ID()))
	for _, entries := range [][]Entry{nil, log.Entries[:len(log.Entries)-1]} {
		store.read = func(context.Context, ProcessID, LSN) iter.Seq2[Entry, error] {
			return func(yield func(Entry, error) bool) {
				for _, entry := range entries {
					if !yield(entry, nil) {
						return
					}
				}
			}
		}
		failures := 0
		for entry, err := range handle.Observe(ctx, 0) {
			if entry.Record != nil || !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("truncated history published entry=%+v error=%v", entry, err)
			}
			failures++
		}
		if failures != 1 {
			t.Fatalf("truncated history reported %d failures, want one", failures)
		}
	}
}

func TestObservationCancellationStopsBufferedDelivery(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entries, failures := 0, 0
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			if !errors.Is(err, context.Canceled) || entry.Record != nil {
				t.Fatalf("cancellation returned entry=%+v error=%v", entry, err)
			}
			failures++
			continue
		}
		entries++
		if entries != 1 {
			t.Fatalf("canceled observation still delivered buffered record %d", entry.LSN)
		}
		cancel()
	}
	if entries != 1 || failures != 1 {
		t.Fatalf("canceled observation returned entries=%d failures=%d", entries, failures)
	}
}

func TestObservationCancellationAtAnEmptyTailDoesNotStopTheWriter(t *testing.T) {
	for _, test := range []struct {
		name  string
		local bool
	}{
		{"local commit notification", true},
		{"another runtime's writer", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				memory := NewMemoryStore()
				store := &publicObservationStore{store: memory}
				writer := kernelRuntime(t, Config{Store: store})
				program := lifecycleEchoProgram(t, writer, "test.observation_cancel")
				request := StartRequest[int]{ID: hostStartID()}
				owned := controlValue(writer.Start(t.Context(), program, request))
				waitForKernelStep(t, t.Context(), owned, 1)
				observer := owned
				if !test.local {
					reader := kernelRuntime(t, Config{Store: store})
					observer = controlValue(reader.Start(t.Context(), program, request))
				}
				head := controlValue(owned.Inspect(t.Context())).Head
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				store.read = func(readContext context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error] {
					return func(yield func(Entry, error) bool) {
						for entry, err := range memory.Read(readContext, root, after) {
							if !yield(entry, err) {
								return
							}
						}
						cancel()
					}
				}
				failures := 0
				for entry, err := range observer.Observe(ctx, head) {
					if entry.Record != nil || entry.LSN != 0 || !errors.Is(err, context.Canceled) {
						t.Fatalf("empty-tail cancellation returned entry=%+v error=%v", entry, err)
					}
					failures++
				}
				if failures != 1 {
					t.Fatalf("empty-tail cancellation reported %d failures, want one", failures)
				}
				store.read = nil
				if current := controlValue(owned.Inspect(t.Context())); current.Head != head {
					t.Fatal("canceling observation changed committed progress")
				}
				if err := owned.Answer(t.Context(), kernelWait().Key, 42); err != nil {
					t.Fatal(err)
				}
				result := controlValue(owned.Await(t.Context()))
				if output, err := result.Output(); err != nil || output != 42 {
					t.Fatalf("observation cancellation stopped the writer: output=%d error=%v", output, err)
				}
			})
		})
	}
}

func TestObservationPropagatesHistoryFailureAfterConfirmingHead(t *testing.T) {
	store := &publicObservationStore{store: NewMemoryStore()}
	runtime := kernelRuntime(t, Config{Store: store})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	head := controlValue(handle.Inspect(t.Context())).Head
	cause := errors.New("committed history is unavailable")
	store.read = func(context.Context, ProcessID, LSN) iter.Seq2[Entry, error] {
		return func(yield func(Entry, error) bool) { yield(Entry{}, cause) }
	}
	failures := 0
	for entry, err := range handle.Observe(t.Context(), 0) {
		if entry.Record != nil || entry.LSN != 0 || !errors.Is(err, cause) {
			t.Fatalf("history failure returned entry=%+v error=%v", entry, err)
		}
		failures++
	}
	if failures != 1 {
		t.Fatalf("history failure reported %d times, want once", failures)
	}
	store.read = nil
	var cursor LSN
	for entry, err := range handle.Observe(t.Context(), 0) {
		if err != nil {
			t.Fatal(err)
		}
		cursor = entry.LSN
	}
	if cursor != head {
		t.Fatal("a failed read changed a subsequent observation")
	}
}

func TestAnotherRuntimeAwaitsTheCommittedExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		memory := NewMemoryStore()
		store := &publicObservationStore{store: memory}
		writer := kernelRuntime(t, Config{Store: store})
		program := lifecycleEchoProgram(t, writer, "test.remote_await")
		request := StartRequest[int]{ID: hostStartID()}
		owned := controlValue(writer.Start(t.Context(), program, request))
		waitForKernelStep(t, t.Context(), owned, 1)
		reader := kernelRuntime(t, Config{Store: store})
		observer := controlValue(reader.Start(t.Context(), program, request))
		polled := make(chan struct{})
		reads := 0
		store.read = func(ctx context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error] {
			reads++
			if reads == 2 {
				close(polled)
			}
			return memory.Read(ctx, root, after)
		}
		type outcome struct {
			result Result[int]
			err    error
		}
		completed := make(chan outcome, 1)
		go func() {
			result, err := observer.Await(t.Context())
			completed <- outcome{result: result, err: err}
		}()
		<-polled
		if err := owned.Answer(t.Context(), kernelWait().Key, 42); err != nil {
			t.Fatal(err)
		}
		observed := <-completed
		if observed.err != nil {
			t.Fatal(observed.err)
		}
		if output, err := observed.result.Output(); err != nil || output != 42 {
			t.Fatalf("remote wait lost the committed result: output=%d error=%v", output, err)
		}
		committed := controlValue(owned.Await(t.Context()))
		if observed.result.ProcessID() != committed.ProcessID() || !observed.result.FinishedAt().Equal(committed.FinishedAt()) || observed.result.Usage() != committed.Usage() {
			t.Fatal("remote wait projected a different terminal outcome")
		}
	})
}
