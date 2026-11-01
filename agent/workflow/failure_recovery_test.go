package workflow_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestNestedWorkflowPreservesMaximumFailureDiagnostic(t *testing.T) {
	const prefix = `transform "fail": `
	cause := strings.Repeat("x", 4096-len(prefix))
	leaf := mustDeployment(t, mustDefinition(t, "test.failure.leaf",
		mustTransform(t, "fail", func(context.Context, numberInput) (numberInput, error) {
			return numberInput{}, errors.New(cause)
		}),
	), "failure-leaf")
	call, err := workflow.Call(workflow.CallConfig{
		ID: "invoke", Deployment: leaf, Budget: run.Budget{Steps: run.NewQuota(8), Effects: run.NewQuota(8), Signals: run.NewQuota(8)},
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := mustDeployment(t, mustDefinition(t, "test.failure.caller", call), "failure-caller")
	selector, err := workflow.Switch(workflow.SwitchConfig[numberInput]{
		ID: "route", Select: func(context.Context, numberInput) (string, error) { return "selected", nil },
		Cases: []workflow.SwitchCase{{ID: "selected", Deployment: caller, Budget: run.Budget{Steps: run.NewQuota(16), Effects: run.NewQuota(16), Signals: run.NewQuota(16)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := mustDeployment(t, mustDefinition(t, "test.failure.router", selector), "failure-router")
	mapper, err := workflow.Map(workflow.MapConfig[numberInput, numberInput]{
		ID: "items", Deployment: router, Budget: run.Budget{Steps: run.NewQuota(32), Effects: run.NewQuota(32), Signals: run.NewQuota(32)},
		WindowSize: 2, MaxItems: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := mustDeployment(t, mustDefinition(t, "test.failure.root", mapper), "failure-root")
	store := run.NewMemoryStore()
	config := run.Config{Store: store}
	engine, err := run.New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := run.EncodePayload([]numberInput{{Value: 1}, {Value: 2}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), engine, root, input)
	if err != nil {
		t.Fatal(err)
	}
	head, present, err := store.LoadTree(t.Context(), result.ProcessID())
	if err != nil || !present || len(head.ProcessSnapshots()) != 7 {
		t.Fatalf("nested failure tree = %t, %v, %d Processes", present, err, len(head.ProcessSnapshots()))
	}
	restoredEngine, err := run.New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := restoredEngine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	restored, err := restoredEngine.Recover(t.Context(), root, head)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restored.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, observed := range []run.Result[run.Payload]{result, recovered} {
		failure, failed := observed.Termination().Failure()
		if !failed || failure.Kind() != run.FailureKindExecution || failure.Code() != "execution.step.failed" || failure.Message() != prefix+cause {
			t.Fatalf("nested failure lost its root cause: %s, %s, %d bytes", failure.Kind(), failure.Code(), len(failure.Message()))
		}
	}
}
