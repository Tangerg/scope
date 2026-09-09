package workflow_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestWorkflowCallbacksReceiveProcessCancellation(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage func(*testing.T, agent.Deployment, chan struct{}) (workflow.Stage, error)
	}{
		{
			name: "transform",
			stage: func(_ *testing.T, _ agent.Deployment, entered chan struct{}) (workflow.Stage, error) {
				return workflow.Transform("work", canceledWorkflowCallback[numberInput, numberInput](entered))
			},
		},
		{
			name: "switch",
			stage: func(t *testing.T, child agent.Deployment, entered chan struct{}) (workflow.Stage, error) {
				return workflow.Switch(workflow.SwitchConfig[numberInput]{
					ID: "work", Select: canceledWorkflowCallback[numberInput, string](entered),
					Cases: []workflow.SwitchCase{{ID: "child", Deployment: child, Budget: mustBudget(t)}},
				})
			},
		},
		{
			name: "fork",
			stage: func(t *testing.T, child agent.Deployment, entered chan struct{}) (workflow.Stage, error) {
				return workflow.Fork(workflow.ForkConfig[numberInput, numberInput, numberInput]{
					ID: "work", WindowSize: 1,
					Branches: []workflow.ForkBranch{{ID: "child", Deployment: child, Budget: mustBudget(t)}},
					Reduce:   canceledWorkflowCallback[[]numberInput, numberInput](entered),
				})
			},
		},
		{
			name: "loop",
			stage: func(t *testing.T, child agent.Deployment, entered chan struct{}) (workflow.Stage, error) {
				return workflow.Loop(workflow.LoopConfig[numberInput]{
					ID: "work", Body: child, Budget: mustBudget(t), MaxIterations: 1,
					Predicate: canceledWorkflowCallback[numberInput, bool](entered),
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				child := mustDeployment(t, mustDefinition(t, "test.workflow.cancel_child",
					mustTransform(t, "identity", func(_ context.Context, input numberInput) (numberInput, error) {
						return input, nil
					}),
				), "cancel-child")
				entered := make(chan struct{})
				stage, err := test.stage(t, child, entered)
				if err != nil {
					t.Fatal(err)
				}
				deployment := mustDeployment(t, mustDefinition(t, "test.workflow.cancel_"+test.name, stage), test.name)
				engine, err := agent.NewEngine(agent.EngineConfig{
					DeploymentResolver: deploymentResolver{child.DeploymentRef(): child},
				})
				if err != nil {
					t.Fatal(err)
				}
				input, _ := agent.EncodeInput(numberInput{Value: 3})
				process, err := engine.Start(t.Context(), deployment, input)
				if err != nil {
					t.Fatal(err)
				}
				<-entered
				if cancellationErr := process.RequestCancellation(t.Context(), "stop workflow computation"); cancellationErr != nil {
					t.Fatal(cancellationErr)
				}
				result, err := process.Await(t.Context())
				if err != nil || result.Status() != agent.StatusCanceled ||
					result.Termination().Cause() != agent.TerminationCauseHostCancellation {
					t.Fatalf("callback cancellation result=%+v, error=%v", result, err)
				}
				if err := engine.Close(); err != nil {
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
	definition := mustDefinition(t, "test.workflow.canceled_step",
		mustTransform(t, "work", func(_ context.Context, input numberInput) (numberInput, error) {
			called = true
			return input, nil
		}),
	)
	input, _ := agent.EncodeInput(numberInput{Value: 3})
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
	if transition, stepErr := execution.Step(ctx, nil); !errors.Is(stepErr, context.Canceled) || transition.Valid() || called {
		t.Fatalf("canceled Step transition=%+v, error=%v, called=%t", transition, stepErr, called)
	}
	after, err := execution.Snapshot()
	if err != nil || !bytes.Equal(before.Payload(), after.Payload()) {
		t.Fatalf("canceled Step changed candidate state: %v", err)
	}
}
