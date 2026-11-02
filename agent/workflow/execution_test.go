package workflow_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestTransformAndCallRunAsManagedChildProcess(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.child",
		mustTransform(t, "double", func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput{Value: input.Value * 2}, nil
		}),
	), "child")
	call, err := workflow.Call(workflow.CallConfig{
		ID: "managed_child", Program: child, Budget: mustBudget(t),
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
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	input, err := run.EncodePayload(numberInput{Value: 2})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(context.Background(), engine, parent, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("Workflow status = %s, termination = %#v", result.Termination().Status(), result.Termination())
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
		ID: "failing_child", Program: child, Budget: mustBudget(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := mustDeployment(t, mustDefinition(t, "test.workflow.failure_parent", call), "failure-parent")
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := run.EncodePayload(numberInput{Value: 2})
	result, err := runtest.Observe(context.Background(), engine, parent, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Termination().Status() != run.StatusFailed {
		t.Fatalf("Workflow status = %s", result.Termination().Status())
	}
	failure, present := result.Termination().Failure()
	if !present || failure.Kind() != run.FailureKindExecution || failure.Code() != "execution.step.failed" ||
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
	_, err := workflow.Call(workflow.CallConfig{ID: "child", Program: child})
	if err != nil {
		t.Fatalf("Call error = %v", err)
	}
}

func TestCompletedWorkflowCannotAdvanceOrChangeItsSnapshot(t *testing.T) {
	definition := mustDefinition(t, "test.workflow.completed",
		mustTransform(t, "identity", func(_ context.Context, value int) (int, error) { return value, nil }),
	)
	input, err := run.EncodePayload(7)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, stepErr := execution.Step(t.Context(), nil); stepErr != nil {
		t.Fatal(stepErr)
	}
	completed, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(t.Context(), completed)
	if err != nil {
		t.Fatal(err)
	}
	if _, stepErr := restored.Step(t.Context(), nil); !errors.Is(stepErr, workflow.ErrInvalidProtocol) {
		t.Fatalf("completed execution Step = %v", stepErr)
	}
	after, err := restored.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Payload()) != string(completed.Payload()) || after.Kind() != completed.Kind() {
		t.Fatalf("completed execution changed its checkpoint: %s -> %s", completed.Payload(), after.Payload())
	}
}

func mustDeployment(t *testing.T, definition run.Definition, identity string) run.Program[run.Payload, run.Payload] {
	t.Helper()
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition:           definition,
		ImplementationDigest: run.ComputeDigest([]byte(identity + "-implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte(identity + "-configuration")),
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
	root := func(child run.Program[run.Payload, run.Payload]) run.Program[run.Payload, run.Payload] {
		stage, err := workflow.Call(workflow.CallConfig{ID: "child", Program: child})
		if err != nil {
			t.Fatal(err)
		}
		definition := mustDefinition(t, "test.workflow.binding_root", stage)
		if bindings := definition.ChildPrograms(); len(bindings) != 1 || bindings[0].ProgramRef() != child.ProgramRef() {
			t.Fatalf("ChildPrograms = %v, want the Call binding", bindings)
		}
		return mustDeployment(t, definition, "binding-root")
	}
	first := mustDeployment(t, mustDefinition(t, "test.workflow.first_child", mustTransform(t, "identity", identity)), "first-child")
	second := mustDeployment(t, mustDefinition(t, "test.workflow.second_child", mustTransform(t, "identity", identity)), "second-child")
	if root(first).ProgramRef() == root(second).ProgramRef() {
		t.Fatal("Workflows bound to different children share one Program identity")
	}
}
