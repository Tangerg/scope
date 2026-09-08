package workflow_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestMapUsesManagedChildrenAndPreservesItemOrder(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.map_child",
		mustTransform(t, "double", func(_ context.Context, input forkInput) (numberOutput, error) {
			return numberOutput{Value: input.Value * 2}, nil
		}),
	), "map-child")
	stage, err := workflow.Map(workflow.MapConfig[forkInput, numberOutput]{
		ID: "items", Deployment: child, Budget: mustBudget(t),
		WindowSize: 2, MaxItems: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stage.Valid() {
		t.Fatalf("Map Stage = %#v", stage)
	}
	deployment := mustDeployment(t, mustDefinition(t, "test.workflow.map", stage), "map")
	engine, err := agent.NewEngine(agent.EngineConfig{
		DeploymentResolver: deploymentResolver{child.DeploymentRef(): child},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := agent.EncodeInput([]forkInput{{Value: 3}, {Value: 1}, {Value: 2}})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	output := decodeCompleted[[]numberOutput](t, result)
	if !slices.Equal(output, []numberOutput{{Value: 6}, {Value: 2}, {Value: 4}}) {
		t.Fatalf("Map output = %#v", output)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMapEmptyInputProducesNonNilEmptyOutput(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.empty_map_child",
		mustTransform(t, "identity", func(_ context.Context, input forkInput) (numberOutput, error) {
			return numberOutput(input), nil
		}),
	), "empty-map-child")
	stage, err := workflow.Map(workflow.MapConfig[forkInput, numberOutput]{
		ID: "items", Deployment: child, Budget: mustBudget(t),
		WindowSize: 1, MaxItems: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := mustDeployment(t, mustDefinition(t, "test.workflow.empty_map", stage), "empty-map")
	engine, _ := agent.NewEngine(agent.EngineConfig{})
	input, _ := agent.EncodeInput([]forkInput{})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	output := decodeCompleted[[]numberOutput](t, result)
	if output == nil || len(output) != 0 {
		t.Fatalf("empty Map output = %#v", output)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMapRejectsInputAboveMaxItemsBeforeStartingChildren(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.limited_map_child",
		mustTransform(t, "identity", func(_ context.Context, input forkInput) (numberOutput, error) {
			return numberOutput(input), nil
		}),
	), "limited-map-child")
	stage, err := workflow.Map(workflow.MapConfig[forkInput, numberOutput]{
		ID: "items", Deployment: child, Budget: mustBudget(t),
		WindowSize: 2, MaxItems: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := mustDeployment(t, mustDefinition(t, "test.workflow.limited_map", stage), "limited-map")
	engine, _ := agent.NewEngine(agent.EngineConfig{})
	input, _ := agent.EncodeInput([]forkInput{{Value: 1}, {Value: 2}, {Value: 3}})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Status() != agent.StatusFailed || !present || failure.Code() != "workflow.map.max_items_exceeded" {
		t.Fatalf("Map termination = %#v", result.Termination())
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMapRequiresExplicitLimitsAndMatchingChildContract(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.valid_map_child",
		mustTransform(t, "identity", func(_ context.Context, input forkInput) (numberOutput, error) {
			return numberOutput(input), nil
		}),
	), "valid-map-child")
	wrong := mustDeployment(t, mustDefinition(t, "test.workflow.wrong_map_child",
		mustTransform(t, "wrong", func(context.Context, textValue) (numberOutput, error) {
			return numberOutput{}, nil
		}),
	), "wrong-map-child")
	valid := workflow.MapConfig[forkInput, numberOutput]{
		ID: "items", Budget: mustBudget(t),
	}
	for name, config := range map[string]workflow.MapConfig[forkInput, numberOutput]{
		"zero window size": {ID: valid.ID, Deployment: child, Budget: valid.Budget, MaxItems: 2},
		"zero max items":   {ID: valid.ID, Deployment: child, Budget: valid.Budget, WindowSize: 1},
		"oversized window": {
			ID: valid.ID, Deployment: child, Budget: valid.Budget, WindowSize: 3, MaxItems: 2,
		},
		"schema mismatch": {
			ID: valid.ID, Deployment: wrong, Budget: valid.Budget, WindowSize: 1, MaxItems: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := workflow.Map(config); !errors.Is(err, workflow.ErrInvalidStage) {
				t.Fatalf("Map error = %v", err)
			}
		})
	}
}
