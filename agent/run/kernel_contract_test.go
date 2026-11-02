package run

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestChildAdmissionReservesCompletionAndExit(t *testing.T) {
	for _, maximum := range []uint64{1, 2} {
		t.Run(strconv.FormatUint(maximum, 10), func(t *testing.T) {
			store := NewMemoryStore()
			runtime := kernelRuntime(t, Config{Store: store, Budget: Budget{Messages: NewQuota(maximum)}})
			program := treeFixtureProgram(t, runtime, 1, false, false, Budget{Messages: NewQuota(0)})
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID(), Input: 0}))
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			result := controlValue(handle.Await(ctx))
			log, _, err := store.Load(ctx, handle.ID())
			if err != nil {
				t.Fatal(err)
			}
			children := 0
			for _, entry := range log.Entries {
				if started, ok := entry.Record.(Started); ok && started.Process != handle.ID() {
					children++
				}
			}
			if maximum == 1 {
				if result.Termination().Status() != StatusFailed || children != 0 || result.Usage().PreparedEffects != 0 {
					t.Fatalf("incomplete reservation dispatched work: status=%s children=%d usage=%+v", result.Termination().Status(), children, result.Usage())
				}
			} else if output, outputErr := result.Output(); outputErr != nil || output != 1 || children != 1 || result.Usage().AcceptedMessages != 2 {
				t.Fatalf("exact reservation failed: output=%d err=%v children=%d usage=%+v", output, outputErr, children, result.Usage())
			}
		})
	}
}

func TestBindingIdentityOwnsBuildRevisionConfigurationAndChildren(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	definition := &treeFixtureDefinition{descriptor: kernelDescriptor(t, "test.identity")}
	configuration := map[string]int{"limit": 3}
	bind := func(revision string, value any) Program[int, int] {
		return controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: revision, Configuration: value}))
	}
	original := bind("one", configuration)
	configuration["limit"] = 7
	if original.ProgramRef() != bind("one", map[string]int{"limit": 3}).ProgramRef() {
		t.Fatal("caller mutation changed frozen configuration identity")
	}
	if original.ProgramRef() == bind("two", map[string]int{"limit": 3}).ProgramRef() || original.ProgramRef() == bind("one", configuration).ProgramRef() {
		t.Fatal("changed declaration reused a ProgramRef")
	}
	other := kernelRuntime(t, Config{Build: "another-build"})
	changed := controlValue(Bind[int, int](other.Compiler(), BindingConfig{Definition: definition, Revision: "one", Configuration: map[string]int{"limit": 3}}))
	if changed.ProgramRef() == original.ProgramRef() {
		t.Fatal("Build did not participate in the reference")
	}
	definition.children = []Program[Payload, Payload]{changed.Erase()}
	if _, err := Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}); !errors.Is(err, ErrInvalidProgram) {
		t.Fatalf("cross-build child accepted: %v", err)
	}
}

func TestCallbackClassificationCannotPanicAfterTheBoundary(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.hostile_error"), step: func(context.Context, Turn) (Transition, error) {
		return Transition{}, hostileError{}
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	result := controlValue(handle.Await(ctx))
	failure, ok := result.Termination().Failure()
	if !ok || failure.Kind() != FailureKindPanic {
		t.Fatalf("unsealed error escaped as %+v", failure)
	}
	broken := kernelRuntime(t, Config{Store: hostileStore{}})
	if _, err := broken.Start(ctx, program, StartRequest[int]{ID: foregroundStartID()}); err == nil {
		t.Fatal("panicking error chain became a successful append")
	}
}

type hostileError struct{}

func (hostileError) Error() string { return "host diagnostic" }
func (hostileError) Unwrap() error { panic("host error traversal") }

type hostileStore struct{}

func (hostileStore) Append(context.Context, Batch) error { return hostileError{} }
func (hostileStore) Load(context.Context, ProcessID) (Log, bool, error) {
	return Log{}, false, nil
}

func TestDeltaSequenceSurvivesSameEpochReplay(t *testing.T) {
	deltas := make(chan Delta, 4)
	runtime := kernelRuntime(t, Config{Deltas: deltas})
	effect := controlValue(NewDispatcherEffect(json.RawMessage(`0`)))
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.delta"), step: func(_ context.Context, turn Turn) (Transition, error) {
		if turn.Step == 1 {
			return Yield(0, []Effect{effect})
		}
		return Complete(uint32(len(turn.Messages)), controlValue(EncodePayload(1)))
	}}
	var calls atomic.Uint32
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicySameIdentity}, dispatch: func(_ context.Context, _ EffectRequest, emit DeltaEmitter) (Settlement, error) {
		emit(json.RawMessage(`"increment"`))
		if calls.Add(1) == 1 {
			return NewSettlement(SettlementStatusUnknown, json.RawMessage(`null`))
		}
		return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`1`))
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one"}))
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	var id EffectID
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if settled, ok := entry.Record.(EffectSettled); ok && settled.Outcome.Status() == SettlementStatusUnknown {
			id = settled.ID
			break
		}
	}
	if err := handle.ReplayUnknownEffect(ctx, id); err != nil {
		t.Fatal(err)
	}
	result := controlValue(handle.Await(ctx))
	if result.Usage().PreparedEffects != 1 || result.Usage().AcceptedMessages != 1 {
		t.Fatalf("physical replay changed logical usage: %+v", result.Usage())
	}
	first, second := <-deltas, <-deltas
	if first.EffectID() != second.EffectID() || first.Epoch() != second.Epoch() || first.EffectSequence() != 1 || second.EffectSequence() != 2 {
		t.Fatalf("duplicate delta coordinates: %v/%v", first, second)
	}
}

func TestContinueDoesNotWaitForUnrelatedPendingWork(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	continued := make(chan struct{})
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.continue"), step: func(_ context.Context, turn Turn) (Transition, error) {
		switch turn.Step {
		case 1:
			return Continue(0, controlValue(NewDispatcherEffect(json.RawMessage(`0`))))
		case 2:
			close(continued)
			return Yield(0, nil, kernelWait())
		default:
			return Complete(uint32(len(turn.Messages)), controlValue(EncodePayload(1)))
		}
	}}
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(ctx context.Context, _ EffectRequest, _ DeltaEmitter) (Settlement, error) {
		select {
		case <-continued:
			return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`0`))
		case <-ctx.Done():
			return Settlement{}, ctx.Err()
		}
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one"}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	if result, err := runtime.Run(ctx, program, 0); err != nil || result != 1 {
		t.Fatalf("Continue waited for unrelated work: output=%d error=%v", result, err)
	}
}

func TestYieldDoesNotSpinOnUnconsumedMessages(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	unexpectedStep := make(chan struct{}, 1)
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.yield"), step: func(_ context.Context, turn Turn) (Transition, error) {
		if len(turn.Messages) == 2 {
			return Complete(2, controlValue(EncodePayload(2)))
		}
		if turn.Step > 2 {
			unexpectedStep <- struct{}{}
		}
		return Yield(0, nil, kernelWait())
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	waitForKernelStep(t, ctx, handle, 1)
	if _, err := handle.DeliverSignals(ctx, Signal{ID: controlValue(ParseSignalID("first")), Payload: controlValue(EncodePayload(1))}); err != nil {
		t.Fatal(err)
	}
	waitForKernelStep(t, ctx, handle, 2)
	const quietWindow = 30 * time.Millisecond
	select {
	case <-unexpectedStep:
		t.Fatal("Yield reprocessed an unchanged inbox")
	case <-time.After(quietWindow):
	}
	if _, err := handle.DeliverSignals(ctx, Signal{ID: controlValue(ParseSignalID("second")), Payload: controlValue(EncodePayload(2))}); err != nil {
		t.Fatal(err)
	}
	result := controlValue(handle.Await(ctx))
	if output, err := result.Output(); err != nil || output != 2 || result.Usage().CommittedSteps != 3 {
		t.Fatalf("Yield lost the unconsumed prefix: output=%d err=%v usage=%+v", output, err, result.Usage())
	}
}

func TestWaitAnswerDuringStepDoesNotReopenOrRejectTheWait(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	entered, release := make(chan struct{}), make(chan struct{})
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.concurrent_answer"), step: func(ctx context.Context, turn Turn) (Transition, error) {
		switch turn.Step {
		case 1:
			return Yield(0, nil, kernelWait())
		case 2:
			close(entered)
			select {
			case <-release:
				return Yield(uint32(len(turn.Messages)), nil, kernelWait())
			case <-ctx.Done():
				return Transition{}, ctx.Err()
			}
		default:
			signal := turn.Messages[0].(Signal)
			return Complete(1, signal.Payload)
		}
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	waitForKernelStep(t, ctx, handle, 1)
	if _, err := handle.DeliverSignals(ctx, Signal{ID: controlValue(ParseSignalID("steering")), Payload: controlValue(EncodePayload(0))}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := handle.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := controlValue(handle.Await(ctx))
	if output, err := result.Output(); err != nil || output != 42 {
		t.Fatalf("concurrent answer changed the transition: output=%d error=%v", output, err)
	}
	view := controlValue(handle.Inspect(ctx))
	if process, _ := view.Process(handle.ID()); len(process.Waits) != 0 {
		t.Fatal("the answered wait reopened")
	}
}

func waitForKernelStep(t *testing.T, ctx context.Context, handle *Handle[int], step uint64) {
	t.Helper()
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if stepped, ok := entry.Record.(Stepped); ok && stepped.Process == handle.ID() && stepped.Step == step {
			return
		}
	}
	t.Fatalf("step %d was never committed", step)
}
