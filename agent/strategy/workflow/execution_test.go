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
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodePayload(numberInput{Value: 2})
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
	erased, present := result.Termination().Output()
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
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := agent.EncodePayload(numberInput{Value: 2})
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

func TestCallAcceptsUnlimitedChildAllocation(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.call_target",
		mustTransform(t, "identity", func(_ context.Context, input numberInput) (numberInput, error) { return input, nil }),
	), "call-target")
	_, err := workflow.Call(workflow.CallConfig{ID: "child", Deployment: child})
	if err != nil {
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

// A Workflow's identity must follow its child bindings on its own: the Host
// configuration below is identical, only the bound child differs.
func TestDefinitionChildBindingsDistinguishDeployments(t *testing.T) {
	identity := func(_ context.Context, input numberInput) (numberInput, error) { return input, nil }
	root := func(child agent.Deployment) agent.Deployment {
		stage, err := workflow.Call(workflow.CallConfig{ID: "child", Deployment: child})
		if err != nil {
			t.Fatal(err)
		}
		definition := mustDefinition(t, "test.workflow.binding_root", stage)
		if bindings := definition.ChildDeployments(); len(bindings) != 1 || bindings[0].DeploymentRef() != child.DeploymentRef() {
			t.Fatalf("ChildDeployments = %v, want the Call binding", bindings)
		}
		return mustDeployment(t, definition, "binding-root")
	}
	first := mustDeployment(t, mustDefinition(t, "test.workflow.first_child", mustTransform(t, "identity", identity)), "first-child")
	second := mustDeployment(t, mustDefinition(t, "test.workflow.second_child", mustTransform(t, "identity", identity)), "second-child")
	if root(first).DeploymentRef() == root(second).DeploymentRef() {
		t.Fatal("Workflows bound to different children share one Deployment identity")
	}
}
