package workflow_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

func TestTransformAndCallRunAsManagedChildProcess(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.child",
		mustTransform(t, "double", func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput{Value: input.Value * 2}, nil
		}),
	), "child")
	call, err := workflow.Call(workflow.CallConfig{
		ID: "managed_child", Deployment: child, Budget: mustBudget(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := mustDeployment(t, mustDefinition(t, "test.workflow.parent",
		mustTransform(t, "increment", func(_ context.Context, input numberInput) (numberInput, error) {
			return numberInput{Value: input.Value + 1}, nil
		}),
		call,
		mustTransform(t, "render", func(_ context.Context, output numberOutput) (textValue, error) {
			return textValue{Text: strconv.Itoa(output.Value)}, nil
		}),
	), "parent")
	engine, err := agent.NewEngine(agent.EngineConfig{
		DeploymentResolver: deploymentResolver{child.DeploymentRef(): child},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(numberInput{Value: 2})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), parent, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("Workflow status = %s, termination = %#v", result.Status(), result.Termination())
	}
	erased, present := result.Output()
	if !present {
		t.Fatal("Workflow completed without Output")
	}
	output, err := erased.Decode[textValue]()
	if err != nil {
		t.Fatal(err)
	}
	if output.Text != "6" {
		t.Fatalf("Workflow output = %#v", output)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestCallPropagatesChildFailure(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.failing_child",
		mustTransform(t, "fail", func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput{}, errors.New("deliberate child failure")
		}),
	), "failing-child")
	call, err := workflow.Call(workflow.CallConfig{
		ID: "failing_child", Deployment: child, Budget: mustBudget(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := mustDeployment(t, mustDefinition(t, "test.workflow.failure_parent", call), "failure-parent")
	engine, err := agent.NewEngine(agent.EngineConfig{
		DeploymentResolver: deploymentResolver{child.DeploymentRef(): child},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := agent.EncodeInput(numberInput{Value: 2})
	result, err := engine.Run(context.Background(), parent, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusFailed {
		t.Fatalf("Workflow status = %s", result.Status())
	}
	failure, present := result.Termination().Failure()
	if !present || failure.Kind() != agent.FailureKindExecution || failure.Code() != "execution.step.failed" ||
		failure.Message() != `transform "fail": deliberate child failure` {
		t.Fatalf("Workflow failure = %#v", failure)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestCallRejectsInvalidChildAllocation(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.call_target",
		mustTransform(t, "identity", func(_ context.Context, input numberInput) (numberInput, error) { return input, nil }),
	), "call-target")
	_, err := workflow.Call(workflow.CallConfig{ID: "child", Deployment: child})
	if !errors.Is(err, workflow.ErrInvalidStage) {
		t.Fatalf("Call error = %v", err)
	}
}

func mustDeployment(t *testing.T, definition agent.Definition, identity string) agent.Deployment {
	t.Helper()
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte(identity + "-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(identity + "-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

type deploymentResolver map[agent.DeploymentRef]agent.Deployment

func (d deploymentResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, found := d[reference]
	if !found {
		return agent.Deployment{}, errors.New("deployment not found")
	}
	return deployment, nil
}
