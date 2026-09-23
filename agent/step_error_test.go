package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestStepErrorDiscardsCandidateAndPreservesFailure(t *testing.T) {
	cause := errors.New("invalid strategy input")
	declared := controlValue(NewFailure(FailureKindContract, "test.input.invalid", "input is invalid"))
	for _, test := range []struct {
		name string
		err  error
		kind FailureKind
		code string
	}{
		{"raw", cause, FailureKindExecution, "execution.step.failed"},
		{"classified", fmt.Errorf("step: %w", &StepError{Failure: declared, Cause: cause}), FailureKindContract, "test.input.invalid"},
		{"invalid classification", &StepError{Cause: cause}, FailureKindContract, "execution.step.failed"},
		{"nil classification", (*StepError)(nil), FailureKindPanic, "execution.step.failed"},
		{"panic takes precedence", &StepError{Failure: declared, Cause: &CallbackPanicError{Operation: "test", Value: cause}}, FailureKindPanic, "execution.step.failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := &rejectedStepDefinition{descriptor: newEngineTestDefinition(t, "test.rejected_step", "complete").Descriptor(), err: test.err}
			engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
			defer mustCloseEngine(t, engine)
			deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
			process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{Value: "input"}))))
			waitForStatus(t, process, StatusPaused)
			request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:rejected-step")), WaitID{}, []byte(`{"value":"pending"}`)))
			if accepted, err := process.DeliverSignals(t.Context(), request); err != nil || !accepted {
				t.Fatalf("delivery=%t %v", accepted, err)
			}
			before := inspectProcessSnapshot(t, process)
			if err := process.Resume(t.Context()); err != nil {
				t.Fatal(err)
			}
			result := controlValue(process.Await(t.Context()))
			if err := process.Join(t.Context()); err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != test.kind || failure.Code() != test.code {
				t.Fatalf("failure=%+v", failure)
			}
			after := inspectProcessSnapshot(t, process)
			if string(after.CommittedExecutionState().Payload()) != string(before.CommittedExecutionState().Payload()) || after.Usage() != before.Usage() {
				t.Fatal("failed Step committed candidate state or work")
			}
			receipts := after.SignalReceipts()
			if len(receipts) != 1 || receipts[0].Consumed() || !receipts[0].Matches(request) {
				t.Fatalf("failed Step consumed input: %+v", receipts)
			}
		})
	}
	if !errors.Is(&StepError{Failure: declared, Cause: cause}, cause) {
		t.Fatal("StepError lost its cause")
	}
}

type rejectedStepDefinition struct {
	descriptor Descriptor
	err        error
}

func (r *rejectedStepDefinition) Descriptor() Descriptor { return r.descriptor }
func (r *rejectedStepDefinition) Start(Payload) (Execution, error) {
	return &rejectedStepExecution{err: r.err}, nil
}
func (r *rejectedStepDefinition) Restore(_ context.Context, state ExecutionState) (Execution, error) {
	phase, err := state.Decode[int]("rejected_step")
	if err != nil {
		return nil, err
	}
	return &rejectedStepExecution{phase: phase, err: r.err}, nil
}

type rejectedStepExecution struct {
	phase int
	err   error
}

func (r *rejectedStepExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	r.phase++
	if r.phase == 1 {
		return Pause(0, "await test input")
	}
	effect, err := NewDispatcherEffect([]byte(`{}`))
	if err != nil {
		return Transition{}, err
	}
	transition, err := Continue(uint32(len(signals)), effect)
	if err != nil {
		return Transition{}, err
	}
	return transition, r.err
}
func (r *rejectedStepExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState("rejected_step", r.phase)
}

// ClassifyStepError is the only conversion from a sentinel to the Step
// contract, so its precedence rules are part of that contract.
func TestClassifyStepErrorOwnsSentinelClassification(t *testing.T) {
	sentinel := NewClassifiedError(FailureKindContract, "test.sentinel.invalid", "test: sentinel rejected")
	if classified := ClassifyStepError(nil); classified != nil {
		t.Fatalf("nil error became %v", classified)
	}
	for _, test := range []struct {
		name string
		err  error
	}{
		{"sentinel", sentinel},
		{"wrapped sentinel", fmt.Errorf("step: %w", sentinel)},
	} {
		t.Run(test.name, func(t *testing.T) {
			sealed, ok := errors.AsType[*StepError](ClassifyStepError(test.err))
			if !ok {
				t.Fatal("sentinel reached the Engine unclassified")
			}
			if sealed.Failure.Kind() != FailureKindContract || sealed.Failure.Code() != "test.sentinel.invalid" {
				t.Fatalf("classification = %s/%s", sealed.Failure.Kind(), sealed.Failure.Code())
			}
			if sealed.Failure.Message() != NormalizeDiagnostic(test.err.Error()) {
				t.Fatalf("diagnostic = %q, want the complete wrapped chain", sealed.Failure.Message())
			}
			if !errors.Is(sealed, sentinel) {
				t.Fatal("classification dropped its sentinel")
			}
		})
	}

	// Engine-owned outcomes outrank Strategy classification, and an error the
	// Strategy did not classify stays unclassified for the Engine fallback.
	for _, test := range []struct {
		name string
		err  error
	}{
		{"cancellation", fmt.Errorf("%w: %w", sentinel, context.Canceled)},
		{"deadline", fmt.Errorf("%w: %w", sentinel, context.DeadlineExceeded)},
		{"contained panic", fmt.Errorf("%w: %w", sentinel, &CallbackPanicError{Operation: "test", Value: "boom"})},
		{"unclassified", errors.New("plain execution failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, sealed := errors.AsType[*StepError](ClassifyStepError(test.err)); sealed {
				t.Fatalf("Strategy classification overrode the Engine-owned outcome of %v", test.err)
			}
		})
	}

	// An already classified error keeps its own Failure instead of acquiring
	// the sentinel its chain still carries.
	declared := controlValue(NewFailure(FailureKindExecution, "test.declared", "declared"))
	classified := ClassifyStepError(fmt.Errorf("step: %w", &StepError{Failure: declared, Cause: sentinel}))
	sealed, ok := errors.AsType[*StepError](classified)
	if !ok || sealed.Failure.Code() != "test.declared" {
		t.Fatalf("already classified error = %v", classified)
	}
}

func TestNewClassifiedErrorRejectsAnUnusableDeclaration(t *testing.T) {
	for _, test := range []struct {
		name    string
		kind    FailureKind
		code    string
		message string
	}{
		{"kind", FailureKindInvalid, "test.code", "message"},
		{"code", FailureKindContract, "Test.Code", "message"},
		{"message", FailureKindContract, "test.code", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("an unusable sentinel declaration was admitted")
				}
			}()
			NewClassifiedError(test.kind, test.code, test.message)
		})
	}
}
