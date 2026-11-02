package workflow

import (
	"bytes"
	"context"
	startrand "crypto/rand"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
)

func TestWorkflowCallbacksReceiveProcessCancellation(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage func(*testing.T, run.Program[run.Payload, run.Payload], chan struct{}) (Stage, error)
	}{
		{
			name: "transform",
			stage: func(_ *testing.T, _ run.Program[run.Payload, run.Payload], entered chan struct{}) (Stage, error) {
				return Transform("work", canceledWorkflowCallback[numberInput, numberInput](entered))
			},
		},
		{
			name: "switch",
			stage: func(t *testing.T, child run.Program[run.Payload, run.Payload], entered chan struct{}) (Stage, error) {
				return Switch(SwitchConfig[numberInput]{
					ID: "work", Select: canceledWorkflowCallback[numberInput, string](entered),
					Cases: []SwitchCase{{ID: "child", Program: child, Budget: mustBudget(t)}},
				})
			},
		},
		{
			name: "fork",
			stage: func(t *testing.T, child run.Program[run.Payload, run.Payload], entered chan struct{}) (Stage, error) {
				return Fork(ForkConfig[numberInput, numberInput, numberInput]{
					ID: "work", WindowSize: 1,
					Branches: []ForkBranch{{ID: "child", Program: child, Budget: mustBudget(t)}},
					Reduce: func(ctx context.Context, _ numberInput, outputs []numberInput) (numberInput, error) {
						return canceledWorkflowCallback[[]numberInput, numberInput](entered)(ctx, outputs)
					},
				})
			},
		},
		{
			name: "loop",
			stage: func(t *testing.T, child run.Program[run.Payload, run.Payload], entered chan struct{}) (Stage, error) {
				return Loop(LoopConfig[numberInput]{
					ID: "work", Body: child, Budget: mustBudget(t), MaxIterations: run.NewQuota(1),
					Predicate: canceledWorkflowCallback[numberInput, bool](entered),
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				child := mustProgram(t, mustDefinition(t, "test.cancel_child",
					mustTransform(t, "identity", func(_ context.Context, input numberInput) (numberInput, error) {
						return input, nil
					}),
				), "cancel-child")
				entered := make(chan struct{})
				stage, err := test.stage(t, child, entered)
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
				<-entered
				if cancellationErr := process.Cancel(t.Context(), "stop workflow computation"); cancellationErr != nil {
					t.Fatal(cancellationErr)
				}
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

func canceledWorkflowCallback[I, O any](entered chan struct{}) func(context.Context, I) (O, error) {
	return func(ctx context.Context, _ I) (output O, err error) {
		close(entered)
		<-ctx.Done()
		return output, ctx.Err()
	}
}

func TestWorkflowRejectsCanceledStepBeforeCallingTransform(t *testing.T) {
	called := false
	definition := mustDefinition(t, "test.canceled_step",
		mustTransform(t, "work", func(_ context.Context, input numberInput) (numberInput, error) {
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
	if err != nil || !bytes.Equal(before.Payload(), after.Payload()) {
		t.Fatalf("canceled Step changed candidate state: %v", err)
	}
}
