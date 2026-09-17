package workflow_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/workflow"
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
		ID: "invoke", Deployment: leaf, Budget: agent.Budget{Steps: agent.NewQuota(8), Effects: agent.NewQuota(8), Signals: agent.NewQuota(8)},
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := mustDeployment(t, mustDefinition(t, "test.failure.caller", call), "failure-caller")
	selector, err := workflow.Switch(workflow.SwitchConfig[numberInput]{
		ID: "route", Select: func(context.Context, numberInput) (string, error) { return "selected", nil },
		Cases: []workflow.SwitchCase{{ID: "selected", Deployment: caller, Budget: agent.Budget{Steps: agent.NewQuota(16), Effects: agent.NewQuota(16), Signals: agent.NewQuota(16)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := mustDeployment(t, mustDefinition(t, "test.failure.router", selector), "failure-router")
	mapper, err := workflow.Map(workflow.MapConfig[numberInput, numberInput]{
		ID: "items", Deployment: router, Budget: agent.Budget{Steps: agent.NewQuota(32), Effects: agent.NewQuota(32), Signals: agent.NewQuota(32)},
		WindowSize: 2, MaxItems: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := mustDeployment(t, mustDefinition(t, "test.failure.root", mapper), "failure-root")
	store := agenttest.NewMemoryTreeDurability()
	config := agent.EngineConfig{TreeDurability: store, DeploymentResolver: deploymentResolver{
		leaf.DeploymentRef(): leaf, caller.DeploymentRef(): caller, router.DeploymentRef(): router,
	}}
	engine, err := agent.NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := agent.EncodePayload([]numberInput{{Value: 1}, {Value: 2}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), root, input)
	if err != nil {
		t.Fatal(err)
	}
	head, present, err := store.LoadTree(t.Context(), result.ProcessID())
	if err != nil || !present || len(head.ProcessSnapshots()) != 7 {
		t.Fatalf("nested failure tree = %t, %v, %d Processes", present, err, len(head.ProcessSnapshots()))
	}
	restoredEngine, err := agent.NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := restoredEngine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	restored, err := restoredEngine.RestoreTree(t.Context(), root, head)
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
	for _, observed := range []agent.Result{result, recovered} {
		failure, failed := observed.Termination().Failure()
		if !failed || failure.Kind() != agent.FailureKindExecution || failure.Code() != "execution.step.failed" || failure.Message() != prefix+cause {
			t.Fatalf("nested failure lost its root cause: %s, %s, %d bytes", failure.Kind(), failure.Code(), len(failure.Message()))
		}
	}
}
