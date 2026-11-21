package run

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// dispatchJournal records, in order, which decorator saw which Effect.
type dispatchJournal struct {
	mu      sync.Mutex
	entries []string
}

func (d *dispatchJournal) record(entry string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = append(d.entries, entry)
}

func (d *dispatchJournal) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.entries...)
}

func (d *dispatchJournal) middleware(name string) DispatchMiddleware {
	return func(next Dispatcher) Dispatcher {
		return DispatcherFunc(func(ctx context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error) {
			d.record(name + " " + request.ID().String())
			return next.Dispatch(ctx, request, emit)
		})
	}
}

func succeedingDispatcher(journal *dispatchJournal) Dispatcher {
	return DispatcherFunc(func(_ context.Context, request EffectRequest, _ DeltaEmitter) (Settlement, error) {
		journal.record("dispatcher " + request.ID().String())
		return NewSettlement(SettlementStatusSucceeded, controlValue(ParsePayload([]byte(`7`))))
	})
}

func TestDispatchMiddlewareDecoratesChildDispatchesOutermostFirst(t *testing.T) {
	journal := &dispatchJournal{}
	runtime := kernelRuntime(t, Config{DispatchMiddlewares: []DispatchMiddleware{journal.middleware("outer"), nil, journal.middleware("inner")}})
	child := lifecycleCallProgram(t, runtime, replayNever, succeedingDispatcher(journal))
	parent := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{
		Definition: &treeFixtureDefinition{descriptor: kernelDescriptor(t, "test.dispatch_parent"), children: []Program[Payload, Payload]{child.Erase()}},
		Revision:   "parent",
	}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	if output := controlValue(runtime.Run(ctx, parent, 0)); output != 1 {
		t.Fatalf("output = %d, want one child exit", output)
	}
	// The parent's child start is a framework Effect, so only the child's call
	// is dispatched.
	entries := journal.snapshot()
	if len(entries) != 3 {
		t.Fatalf("dispatches = %v, want one decorated child dispatch", entries)
	}
	effect := entries[2][len("dispatcher "):]
	if entries[0] != "outer "+effect || entries[1] != "inner "+effect {
		t.Fatalf("dispatches = %v, want outer, inner, then the dispatcher", entries)
	}
	if id := controlValue(ParseEffectID(effect)); id.Process.Depth() != 1 {
		t.Fatalf("decorated Effect %v does not belong to the child", id)
	}
}

func TestDispatchMiddlewareDecoratesRecoveryAndReplay(t *testing.T) {
	t.Run("recovery", func(t *testing.T) {
		store := NewMemoryStore()
		cause := errors.New("settlement write interrupted")
		original := &dispatchJournal{}
		runtime := kernelRuntime(t, Config{
			Store:               &lifecycleFaultStore{store: store, kind: RecordKindEffectSettled, cause: cause},
			DispatchMiddlewares: []DispatchMiddleware{original.middleware("original")},
		})
		dispatcher := succeedingDispatcher(&dispatchJournal{})
		program := lifecycleCallProgram(t, runtime, replaySameIdentity, dispatcher)
		handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
		ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
		defer cancel()
		if _, err := handle.Await(ctx); !errors.Is(err, cause) {
			t.Fatal(err)
		}
		recovered := &dispatchJournal{}
		replacement := kernelRuntime(t, Config{Store: store, DispatchMiddlewares: []DispatchMiddleware{recovered.middleware("recovered")}})
		if output := controlValue(controlValue(controlValue(replacement.Recover(ctx, program, handle.ID())).Await(ctx)).Output()); output != 7 {
			t.Fatalf("output = %d", output)
		}
		if len(original.snapshot()) != 1 || len(recovered.snapshot()) != 1 {
			t.Fatalf("original=%v recovered=%v, want the redispatch decorated", original.snapshot(), recovered.snapshot())
		}
	})
	t.Run("unknown replay", func(t *testing.T) {
		journal := &dispatchJournal{}
		runtime := kernelRuntime(t, Config{DispatchMiddlewares: []DispatchMiddleware{journal.middleware("observer")}})
		var calls atomic.Int32
		dispatcher := DispatcherFunc(func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
			if calls.Add(1) == 1 {
				return NewSettlement(SettlementStatusUnknown, nullPayload)
			}
			return NewSettlement(SettlementStatusSucceeded, controlValue(ParsePayload([]byte(`7`))))
		})
		handle := controlValue(runtime.Start(t.Context(), lifecycleCallProgram(t, runtime, replaySameIdentity, dispatcher), StartRequest[int]{ID: hostStartID()}))
		ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
		defer cancel()
		id := lifecycleUnknown(t, ctx, handle)
		if err := handle.ReplayUnknownEffect(ctx, id); err != nil {
			t.Fatal(err)
		}
		if output := controlValue(controlValue(handle.Await(ctx)).Output()); output != 7 {
			t.Fatalf("output = %d", output)
		}
		if entries := journal.snapshot(); len(entries) != 2 || entries[0] != entries[1] {
			t.Fatalf("dispatches = %v, want the replay decorated under the same identity", entries)
		}
	})
}

func TestDispatchMiddlewareOutcomesAreAdmittedLikeADispatchers(t *testing.T) {
	cause := errors.New("middleware unavailable")
	for _, test := range []struct {
		name       string
		middleware DispatchMiddleware
		kind       FailureKind
		code       string
	}{
		{"invalid settlement", func(Dispatcher) Dispatcher {
			return DispatcherFunc(func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) { return Settlement{}, nil })
		}, FailureKindContract, failureRuntimeDispatchSettlementInvalid.Code()},
		{"error", func(Dispatcher) Dispatcher {
			return DispatcherFunc(func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) { return Settlement{}, cause })
		}, FailureKindExternal, failureRuntimeDispatchFailed.Code()},
		{"factory panic", func(Dispatcher) Dispatcher { panic(cause) }, FailureKindPanic, failureRuntimeDispatchFailed.Code()},
		{"no dispatcher", func(Dispatcher) Dispatcher { return nil }, FailureKindPanic, failureRuntimeDispatchFailed.Code()},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := kernelRuntime(t, Config{DispatchMiddlewares: []DispatchMiddleware{test.middleware}})
			var calls atomic.Int32
			dispatcher := DispatcherFunc(func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
				calls.Add(1)
				return NewSettlement(SettlementStatusSucceeded, controlValue(ParsePayload([]byte(`7`))))
			})
			handle := controlValue(runtime.Start(t.Context(), lifecycleCallProgram(t, runtime, replayNever, dispatcher), StartRequest[int]{ID: hostStartID()}))
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			lifecycleUnknown(t, ctx, handle)
			process, found := controlValue(handle.Inspect(ctx)).Process(handle.ID())
			if !found || len(process.PendingEffects) != 1 || calls.Load() != 0 {
				t.Fatalf("middleware outcome was not left Unknown: %+v calls=%d", process, calls.Load())
			}
			if failure := process.PendingEffects[0].Failure; failure == nil || failure.Kind() != test.kind || failure.Code() != test.code {
				t.Fatalf("Unknown outcome failure = %+v, want %s %s", failure, test.kind, test.code)
			}
		})
	}
	t.Run("short circuit", func(t *testing.T) {
		cached := func(Dispatcher) Dispatcher {
			return DispatcherFunc(func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
				return NewSettlement(SettlementStatusSucceeded, controlValue(ParsePayload([]byte(`7`))))
			})
		}
		runtime := kernelRuntime(t, Config{DispatchMiddlewares: []DispatchMiddleware{cached}})
		var calls atomic.Int32
		dispatcher := DispatcherFunc(func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
			calls.Add(1)
			return Settlement{}, cause
		})
		ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
		defer cancel()
		if output := controlValue(runtime.Run(ctx, lifecycleCallProgram(t, runtime, replayNever, dispatcher), 0)); output != 7 || calls.Load() != 0 {
			t.Fatalf("output=%d dispatcher calls=%d, want the middleware's settlement", output, calls.Load())
		}
	})
}

func TestReplayRejectsAStepThatChangesItsEffectPolicy(t *testing.T) {
	store := NewMemoryStore()
	cause := errors.New("settlement write interrupted")
	runtime := kernelRuntime(t, Config{Store: &lifecycleFaultStore{store: store, kind: RecordKindEffectSettled, cause: cause}})
	dispatcher := succeedingDispatcher(&dispatchJournal{})
	handle := controlValue(runtime.Start(t.Context(), lifecycleCallProgram(t, runtime, replayNever, dispatcher), StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	if _, err := handle.Await(ctx); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	replacement := kernelRuntime(t, Config{Store: store})
	escalated := lifecycleCallProgram(t, replacement, replaySameIdentity, dispatcher)
	if _, err := replacement.Recover(ctx, escalated, handle.ID()); !errors.Is(err, ErrNondeterministicStep) {
		t.Fatalf("recovery accepted a Step that changed its committed replay policy: %v", err)
	}
}

func TestStepEncodesEachEffectOnce(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	marker := []byte("payload-written-exactly-once")
	effect := controlValue(NewDispatcherEffect(controlValue(EncodePayload(string(marker))), replayNever))
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.single_encoding"), step: func(_ context.Context, turn Turn) (Transition, error) {
		if turn.Step == 1 {
			return Yield(0, []Effect{effect})
		}
		return Complete(uint32(len(turn.Messages)), controlValue(EncodePayload(7)))
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: succeedingDispatcher(&dispatchJournal{}), Revision: "one"}))
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	controlValue(handle.Await(ctx))
	log, _, err := store.Load(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	occurrences := 0
	for _, entry := range log.Entries {
		_, encoded, err := EncodeRecord(entry.Record)
		if err != nil {
			t.Fatal(err)
		}
		occurrences += bytes.Count(encoded, marker)
	}
	if occurrences != 1 {
		t.Fatalf("the Effect payload was encoded %d times, want once", occurrences)
	}
}
