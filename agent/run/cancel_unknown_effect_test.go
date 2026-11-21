package run_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const externalCallStateKind = "test.external_call"

type externalCallDefinition struct {
	descriptor run.Descriptor
	effect     run.Effect
}

func (e externalCallDefinition) Descriptor() run.Descriptor                           { return e.descriptor }
func (externalCallDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (e externalCallDefinition) Start(run.Payload) (run.Execution, error) {
	return externalCallExecution(e), nil
}

func (e externalCallDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	if _, err := state.Decode[int](externalCallStateKind); err != nil {
		return nil, err
	}
	return externalCallExecution(e), nil
}

type externalCallExecution externalCallDefinition

func (externalCallExecution) Snapshot() (run.ExecutionState, error) {
	payload, err := run.EncodePayload(0)
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.NewExecutionState(externalCallStateKind, payload)
}

func (e externalCallExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	if turn.Step == 1 {
		return run.Yield(0, []run.Effect{e.effect})
	}
	output, err := run.EncodePayload(7)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(uint32(len(turn.Messages)), output)
}

// blockingDispatcher never settles on its own, so the outcome of the Effect it
// starts reaches the log only if the writer survives.
type blockingDispatcher struct {
	entered chan run.EffectID
	calls   *atomic.Int32
}

func (b blockingDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	b.calls.Add(1)
	b.entered <- request.ID()
	<-ctx.Done()
	return run.Settlement{}, ctx.Err()
}

// lostCancelAcknowledgement commits the first Host cancellation and then
// reports the append as failed, as a transport that loses the acknowledgement.
type lostCancelAcknowledgement struct {
	run.Store
	cause error
	lost  atomic.Bool
}

func (l *lostCancelAcknowledgement) Append(ctx context.Context, batch run.Batch) error {
	for _, record := range batch.Records {
		control, ok := record.(run.ControlRequested)
		if !ok || control.Origin != run.ControlOriginHost || control.Kind != run.ControlCancel || !l.lost.CompareAndSwap(false, true) {
			continue
		}
		if err := l.Store.Append(ctx, batch); err != nil {
			return err
		}
		return l.cause
	}
	return l.Store.Append(ctx, batch)
}

func externalCallProgram(t *testing.T, runtime *run.Runtime, replay run.ReplayPolicy, dispatcher run.Dispatcher) run.Program[int, int] {
	t.Helper()
	schema, err := jsonschema.For[int]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: "test.external_call", Description: "Await one external call.", InputSchema: schema, OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := run.ParsePayload([]byte(`0`))
	if err != nil {
		t.Fatal(err)
	}
	effect, err := run.NewDispatcherEffect(payload, run.EffectPolicy{Replay: replay})
	if err != nil {
		t.Fatal(err)
	}
	program, err := run.Bind[int, int](runtime.Compiler(), run.BindingConfig{Definition: externalCallDefinition{descriptor: descriptor, effect: effect}, Dispatcher: dispatcher, Revision: "one"})
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func TestRecoveredCancellationExitsWithItsEffectStillUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cause := errors.New("cancellation acknowledgement lost")
	store := &lostCancelAcknowledgement{Store: run.NewMemoryStore(), cause: cause}
	var calls atomic.Int32
	dispatcher := blockingDispatcher{entered: make(chan run.EffectID, 1), calls: &calls}

	original, err := run.New(ctx, run.Config{Store: store, Build: "cancel-unknown"})
	if err != nil {
		t.Fatal(err)
	}
	program := externalCallProgram(t, original, run.ReplayPolicyNever, dispatcher)
	handle, err := original.Start(ctx, program, run.StartRequest[int]{ID: run.StartID{Scope: "test", Key: "cancel-unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	var effect run.EffectID
	select {
	case effect = <-dispatcher.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if cancelErr := handle.Cancel(ctx, "user canceled"); !errors.Is(cancelErr, cause) {
		t.Fatalf("Cancel = %v, want the lost acknowledgement", cancelErr)
	}
	if closeErr := original.Close(ctx); closeErr != nil {
		t.Fatal(closeErr)
	}

	replacement, err := run.New(ctx, run.Config{Store: store, Build: "cancel-unknown"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Close(context.WithoutCancel(ctx)) })
	recovered, err := replacement.Recover(ctx, program, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	result, err := recovered.Await(ctx)
	if err != nil {
		inspection, _ := replacement.Inspect(context.WithoutCancel(ctx), handle.ID())
		t.Fatalf("Await = %v; inspection %+v", err, inspection)
	}
	termination := result.Termination()
	if termination.Status() != run.StatusCanceled || termination.Cause() != run.TerminationCauseHostCancellation || termination.Reason() != "user canceled" {
		t.Fatalf("termination = %+v, want the original Host cancellation", termination)
	}

	log, err := replacement.History(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	var recovery, unknownAt, exitedAt run.LSN
	var recoveredIntents int
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Activated:
			if record.Epoch == recovered.Epoch() {
				recovery = entry.LSN
			}
		case run.Stepped:
			if recovery != 0 {
				recoveredIntents += len(record.Intents())
			}
		case run.EffectSettled:
			if record.ID != effect || record.Outcome.Status() != run.SettlementStatusUnknown {
				t.Fatalf("settlement %+v, want only effect %v settled Unknown", record, effect)
			}
			unknownAt = entry.LSN
		case run.Exited:
			if record.Process == handle.ID() {
				exitedAt = entry.LSN
				if record.Termination.Cause() != termination.Cause() || record.Termination.Reason() != termination.Reason() {
					t.Fatalf("logged Exit %+v differs from the Result %+v", record.Termination, termination)
				}
			}
		}
	}
	if recovery == 0 || unknownAt <= recovery || exitedAt <= unknownAt {
		t.Fatalf("recovery=%d unknown=%d exited=%d, want recovery to settle the Effect Unknown before the Exit", recovery, unknownAt, exitedAt)
	}
	if recoveredIntents != 0 || calls.Load() != 1 {
		t.Fatalf("recovery prepared %d Effects and dispatched %d calls in total, want 0 and 1", recoveredIntents, calls.Load())
	}

	inspection, err := replacement.Inspect(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	process, found := inspection.Process(handle.ID())
	if !found || process.Status != run.StatusCanceled || len(process.PendingEffects) != 1 || process.PendingEffects[0].Request.ID() != effect || process.PendingEffects[0].Outcome.Status() != run.SettlementStatusUnknown {
		t.Fatalf("exited process lost its Unknown Effect: %+v", process)
	}
}

type unknownDispatcher struct{}

func (unknownDispatcher) Dispatch(context.Context, run.EffectRequest, run.DeltaEmitter) (run.Settlement, error) {
	null, err := run.ParsePayload([]byte(`null`))
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusUnknown, null)
}

func TestExitedProcessAnswersEveryCommandWithProcessFinished(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime, err := run.New(ctx, run.Config{Store: run.NewMemoryStore(), Build: "exited-commands"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.WithoutCancel(ctx)) })
	handle, err := runtime.Start(ctx, externalCallProgram(t, runtime, run.ReplayPolicySameIdentity, unknownDispatcher{}), run.StartRequest[int]{ID: run.StartID{Scope: "test", Key: "exited-commands"}})
	if err != nil {
		t.Fatal(err)
	}
	var effect run.EffectID
	for entry, observeErr := range handle.Observe(ctx, 0) {
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		if settled, ok := entry.Record.(run.EffectSettled); ok {
			effect = settled.ID
			break
		}
	}
	if cancelErr := handle.Cancel(ctx, "stop with the outcome unknown"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if _, awaitErr := handle.Await(ctx); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	seven, err := run.EncodePayload(7)
	if err != nil {
		t.Fatal(err)
	}
	succeeded, err := run.NewSettlement(run.SettlementStatusSucceeded, seven)
	if err != nil {
		t.Fatal(err)
	}
	for name, command := range map[string]func() error{
		"cancel":  func() error { return handle.Cancel(ctx, "again") },
		"kill":    func() error { return handle.Kill(ctx, "again") },
		"pause":   func() error { return handle.Pause(ctx, "again") },
		"resolve": func() error { return handle.ResolveUnknownEffect(ctx, effect, succeeded) },
		"replay":  func() error { return handle.ReplayUnknownEffect(ctx, effect) },
	} {
		if err := command(); !errors.Is(err, run.ErrProcessFinished) {
			t.Errorf("%s after Exit = %v, want ErrProcessFinished", name, err)
		}
	}
	// Argument errors are decided at the Handle, before process state.
	if err := handle.Cancel(ctx, " untrimmed "); !errors.Is(err, run.ErrInvalidProcessControl) {
		t.Errorf("invalid reason after Exit = %v, want ErrInvalidProcessControl", err)
	}
}
