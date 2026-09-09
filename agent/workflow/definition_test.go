package workflow_test

import (
	"context"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

type numberInput struct {
	Value int `json:"value"`
}

type numberOutput struct {
	Value int `json:"value"`
}

type textValue struct {
	Text string `json:"text"`
}

func TestDefinitionRequiresUniqueConnectedStages(t *testing.T) {
	first := mustTransform(t, "first", func(_ context.Context, input numberInput) (numberOutput, error) {
		return numberOutput(input), nil
	})
	duplicate := mustTransform(t, "first", func(_ context.Context, input numberOutput) (numberOutput, error) {
		return input, nil
	})
	disconnected := mustTransform(t, "text", func(_ context.Context, input textValue) (textValue, error) {
		return input, nil
	})

	for name, stages := range map[string][]workflow.Stage{
		"empty":        nil,
		"duplicate":    {first, duplicate},
		"disconnected": {first, disconnected},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := workflow.NewDefinition(workflow.DefinitionConfig{
				Name: "test.invalid", Description: "Reject an invalid Workflow Definition.",
				Stages: stages,
			})
			if !errors.Is(err, workflow.ErrInvalidDefinitionConfig) {
				t.Fatalf("NewDefinition error = %v", err)
			}
		})
	}
}

func TestStageConstructorsExposeAccurateImmutableContracts(t *testing.T) {
	transform := mustTransform(t, "increment", func(_ context.Context, input numberInput) (numberOutput, error) {
		return numberOutput{Value: input.Value + 1}, nil
	})
	if !transform.Valid() {
		t.Fatalf("Transform Stage = %#v", transform)
	}
	if _, err := workflow.Transform("Invalid ID", func(_ context.Context, input numberInput) (numberOutput, error) {
		return numberOutput(input), nil
	}); !errors.Is(err, workflow.ErrInvalidStage) {
		t.Fatalf("invalid Transform error = %v", err)
	}
	var nilTransform workflow.TransformFunc[numberInput, numberOutput]
	if _, err := workflow.Transform("nil", nilTransform); !errors.Is(err, workflow.ErrInvalidStage) {
		t.Fatalf("nil Transform error = %v", err)
	}
}

func mustDefinition(t *testing.T, name string, stages ...workflow.Stage) *workflow.Definition {
	t.Helper()
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: name, Description: "Execute a deterministic managed Workflow.",
		Stages: stages,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func mustTransform[I, O any](t *testing.T, id string, transform workflow.TransformFunc[I, O]) workflow.Stage {
	t.Helper()
	stage, err := workflow.Transform(id, transform)
	if err != nil {
		t.Fatal(err)
	}
	return stage
}

func mustBudget(t *testing.T) agent.Budget {
	t.Helper()
	budget, err := agent.NewBudget(agent.BudgetConfig{Steps: 64, Effects: 64, Signals: 64})
	if err != nil {
		t.Fatal(err)
	}
	return budget
}
