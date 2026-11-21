package workflow

import (
	"context"
	startrand "crypto/rand"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
)

// A callback takes no context, so a process canceled while one runs ends
// once the callback returns, and the Runtime discards that Step's result.
func TestCancellationDuringACallbackDiscardsItsStep(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage func(*testing.T, run.Program[run.Payload, run.Payload], workflowCallbackGate) (Stage, error)
	}{
		{
			name: "transform",
			stage: func(_ *testing.T, _ run.Program[run.Payload, run.Payload], gate workflowCallbackGate) (Stage, error) {
				return Transform("work", blockingWorkflowCallback[numberInput, numberInput](gate))
			},
		},
		{
			name: "switch",
			stage: func(t *testing.T, child run.Program[run.Payload, run.Payload], gate workflowCallbackGate) (Stage, error) {
				return Switch(SwitchConfig[numberInput]{
					ID: "work", Select: blockingWorkflowCallback[numberInput, string](gate),
					Cases: []SwitchCase{{ID: "child", Child: run.ChildBinding{Program: child, Budget: mustBudget(t)}}},
				})
			},
		},
		{
			name: "fork",
			stage: func(t *testing.T, child run.Program[run.Payload, run.Payload], gate workflowCallbackGate) (Stage, error) {
				return Fork(ForkConfig[numberInput, numberInput, numberInput]{
					ID: "work", WindowSize: 1,
					Branches: []ForkBranch{{ID: "child", Child: run.ChildBinding{Program: child, Budget: mustBudget(t)}}},
					Reduce: func(_ numberInput, outputs []numberInput) (numberInput, error) {
						return blockingWorkflowCallback[[]numberInput, numberInput](gate)(outputs)
					},
				})
			},
		},
		{
			name: "loop",
			stage: func(t *testing.T, child run.Program[run.Payload, run.Payload], gate workflowCallbackGate) (Stage, error) {
				return Loop(LoopConfig[numberInput]{
					ID: "work", Body: run.ChildBinding{Program: child, Budget: mustBudget(t)}, MaxIterations: run.NewQuota(1),
					Predicate: blockingWorkflowCallback[numberInput, bool](gate),
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				child := mustProgram(t, mustDefinition(t, "test.cancel_child",
					mustTransform(t, "identity", func(input numberInput) (numberInput, error) {
						return input, nil
					}),
				), "cancel-child")
				gate := workflowCallbackGate{entered: make(chan struct{}), release: make(chan struct{})}
				stage, err := test.stage(t, child, gate)
				if err != nil {
					t.Fatal(err)
				}
				program := mustProgram(t, mustDefinition(t, "test.cancel_"+test.name, stage), test.name)
				runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
				if err != nil {
					t.Fatal(err)
				}
				input, _ := run.EncodePayload(numberInput{Value: 3})
				process, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
				if err != nil {
					t.Fatal(err)
				}
				<-gate.entered
				if cancellationErr := process.Cancel(t.Context(), "stop workflow computation"); cancellationErr != nil {
					t.Fatal(cancellationErr)
				}
				close(gate.release)
				result, err := process.Await(t.Context())
				if err != nil || result.Termination().Status() != run.StatusCanceled ||
					result.Termination().Cause() != run.TerminationCauseHostCancellation {
					t.Fatalf("callback cancellation result=%+v, error=%v", result, err)
				}
				if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

// workflowCallbackGate holds a callback until the test has canceled its
// process.
type workflowCallbackGate struct {
	entered chan struct{}
	release chan struct{}
}

func blockingWorkflowCallback[I, O any](gate workflowCallbackGate) func(I) (O, error) {
	return func(I) (output O, err error) {
		close(gate.entered)
		<-gate.release
		return output, nil
	}
}

func TestWorkflowRejectsCanceledStepBeforeCallingTransform(t *testing.T) {
	called := false
	definition := mustDefinition(t, "test.canceled_step",
		mustTransform(t, "work", func(input numberInput) (numberInput, error) {
			called = true
			return input, nil
		}),
	)
	input, _ := run.EncodePayload(numberInput{Value: 3})
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	before, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if transition, stepErr := execution.Step(ctx, testTurn(1)); !errors.Is(stepErr, context.Canceled) || transition.Valid() || called {
		t.Fatalf("canceled Step transition=%+v, error=%v, called=%t", transition, stepErr, called)
	}
	after, err := execution.Snapshot()
	if err != nil || !before.Payload().Equal(after.Payload()) {
		t.Fatalf("canceled Step changed candidate state: %v", err)
	}
}
