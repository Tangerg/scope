package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestClassifiedStepErrorDiscardsCandidateAndPreservesFailure(t *testing.T) {
	cause := errors.New("invalid strategy input")
	sentinel := NewClassifiedError(FailureKindContract, "test.input.invalid", "test: input is invalid")
	for _, test := range []struct {
		name string
		err  error
		kind FailureKind
		code string
	}{
		{"raw", cause, FailureKindExecution, "execution.step.failed"},
		{"classified", fmt.Errorf("step: %w: %w", sentinel, cause), FailureKindContract, "test.input.invalid"},
		{"panic takes precedence", fmt.Errorf("%w: %w", sentinel, &CallbackPanicError{Operation: "test", Value: cause}), FailureKindPanic, "execution.step.failed"},
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
}

type rejectedStepDefinition struct {
	descriptor Descriptor
	err        error
}

func (r *rejectedStepDefinition) Descriptor() Descriptor { return r.descriptor }

func (*rejectedStepDefinition) ChildDeployments() []Deployment { return nil }
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

func TestClassifiedErrorOwnsStepClassification(t *testing.T) {
	sentinel := NewClassifiedError(FailureKindContract, "test.sentinel.invalid", "test: sentinel rejected")
	if _, classified := StepFailure(nil); classified {
		t.Fatal("nil error was classified")
	}
	for _, test := range []struct {
		name string
		err  error
	}{
		{"sentinel", sentinel},
		{"wrapped sentinel", fmt.Errorf("step: %w", sentinel)},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure, ok := StepFailure(test.err)
			if !ok {
				t.Fatal("sentinel reached the Engine unclassified")
			}
			if failure.Kind() != FailureKindContract || failure.Code() != "test.sentinel.invalid" {
				t.Fatalf("classification = %s/%s", failure.Kind(), failure.Code())
			}
			if failure.Message() != NormalizeDiagnostic(test.err.Error()) {
				t.Fatalf("diagnostic = %q, want the complete wrapped chain", failure.Message())
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
			if _, classified := StepFailure(test.err); classified {
				t.Fatalf("Strategy classification overrode the Engine-owned outcome of %v", test.err)
			}
		})
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
