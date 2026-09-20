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
			engine := controlValue(NewEngine(EngineConfig{}))
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
