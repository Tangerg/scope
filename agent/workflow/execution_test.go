package workflow

import (
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestTransformAndCallRunAsManagedChildProcess(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "test.child",
		mustTransform(t, "double", func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput{Value: input.Value * 2}, nil
		}),
	), "child")
	call, err := Call(CallConfig{
		ID: "managed_child", Program: child, Budget: mustBudget(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := mustProgram(t, mustDefinition(t, "test.parent",
		mustTransform(t, "increment", func(_ context.Context, input numberInput) (numberInput, error) {
			return numberInput{Value: input.Value + 1}, nil
		}),
		call,
		mustTransform(t, "render", func(_ context.Context, output numberOutput) (textValue, error) {
			return textValue{Text: strconv.Itoa(output.Value)}, nil
		}),
	), "parent")
	runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	input, err := run.EncodePayload(numberInput{Value: 2})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(context.Background(), runtime, parent, input)
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
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestCallPropagatesChildFailure(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "test.failing_child",
		mustTransform(t, "fail", func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput{}, errors.New("deliberate child failure")
		}),
	), "failing-child")
	call, err := Call(CallConfig{
		ID: "failing_child", Program: child, Budget: mustBudget(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := mustProgram(t, mustDefinition(t, "test.failure_parent", call), "failure-parent")
	runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := run.EncodePayload(numberInput{Value: 2})
	result, err := runtest.Observe(context.Background(), runtime, parent, input)
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
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestCallAcceptsUnlimitedChildAllocation(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "test.call_target",
		mustTransform(t, "identity", func(_ context.Context, input numberInput) (numberInput, error) { return input, nil }),
	), "call-target")
	_, err := Call(CallConfig{ID: "child", Program: child})
	if err != nil {
		t.Fatalf("Call error = %v", err)
	}
}

func TestCompletedWorkflowCannotAdvanceOrChangeItsSnapshot(t *testing.T) {
	definition := mustDefinition(t, "test.completed",
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
	if _, stepErr := execution.Step(t.Context(), testTurn(1)); stepErr != nil {
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
	if _, stepErr := restored.Step(t.Context(), testTurn(1)); !errors.Is(stepErr, ErrInvalidProtocol) {
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

func mustProgram(t *testing.T, definition run.Definition, identity string) run.Program[run.Payload, run.Payload] {
	t.Helper()
	program, err := run.Bind[run.Payload, run.Payload](conformancetest.Compiler(t), run.BindingConfig{
		Definition:    definition,
		Revision:      identity + "-implementation",
		Configuration: identity + "-configuration",
	})
	if err != nil {
		t.Fatal(err)
	}
	return program
}

// A Workflow's identity must follow its child bindings on its own: the Host
// configuration below is identical, only the bound child differs.
func TestDefinitionChildBindingsDistinguishPrograms(t *testing.T) {
	identity := func(_ context.Context, input numberInput) (numberInput, error) { return input, nil }
	root := func(child run.Program[run.Payload, run.Payload]) run.Program[run.Payload, run.Payload] {
		stage, err := Call(CallConfig{ID: "child", Program: child})
		if err != nil {
			t.Fatal(err)
		}
		definition := mustDefinition(t, "test.binding_root", stage)
		if bindings := definition.ChildPrograms(); len(bindings) != 1 || bindings[0].ProgramRef() != child.ProgramRef() {
			t.Fatalf("ChildPrograms = %v, want the Call binding", bindings)
		}
		return mustProgram(t, definition, "binding-root")
	}
	first := mustProgram(t, mustDefinition(t, "test.first_child", mustTransform(t, "identity", identity)), "first-child")
	second := mustProgram(t, mustDefinition(t, "test.second_child", mustTransform(t, "identity", identity)), "second-child")
	if root(first).ProgramRef() == root(second).ProgramRef() {
		t.Fatal("Workflows bound to different children share one Program identity")
	}
}
