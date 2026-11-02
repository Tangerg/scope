package workflow

import (
	"context"
	startrand "crypto/rand"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
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

	for name, stages := range map[string][]Stage{
		"empty":        nil,
		"zero":         {Stage{}},
		"duplicate":    {first, duplicate},
		"disconnected": {first, disconnected},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newDefinition(definitionConfig{
				Name: "test.invalid", Description: "Reject an invalid Workflow definition.",
				Stages: stages,
			})
			if !errors.Is(err, ErrInvalidDefinitionConfig) {
				t.Fatalf("newDefinition error = %v", err)
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
	if _, err := Transform("Invalid ID", func(_ context.Context, input numberInput) (numberOutput, error) {
		return numberOutput(input), nil
	}); !errors.Is(err, ErrInvalidStage) {
		t.Fatalf("invalid Transform error = %v", err)
	}
	var nilTransform TransformFunc[numberInput, numberOutput]
	if _, err := Transform("nil", nilTransform); !errors.Is(err, ErrInvalidStage) {
		t.Fatalf("nil Transform error = %v", err)
	}
}

func TestRuntimeOwnsExactRestoreBindingForCompatibleWorkflows(t *testing.T) {
	stage := mustTransform(t, "identity", func(_ context.Context, input numberInput) (numberInput, error) {
		return input, nil
	})
	first := mustProgram(t, mustDefinition(t, "first", stage), "shared-implementation")
	second := mustProgram(t, mustDefinition(t, "second", stage), "shared-implementation")
	runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := run.EncodePayload(numberInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	root, err := runtime.Start(t.Context(), first, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if _, joinErr := root.Await(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	if _, err := runtime.Recover(t.Context(), second, root.ID()); !errors.Is(err, run.ErrInvalidProgram) {
		t.Fatalf("different program accepted stored execution: %v", err)
	}

}

func mustDefinition(t *testing.T, name string, stages ...Stage) *definition {
	t.Helper()
	definition, err := newDefinition(definitionConfig{
		Name: name, Description: "Execute a deterministic managed Workflow.",
		Stages: stages,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func mustTransform[I, O any](t *testing.T, id string, transform TransformFunc[I, O]) Stage {
	t.Helper()
	stage, err := Transform(id, transform)
	if err != nil {
		t.Fatal(err)
	}
	return stage
}

func mustBudget(t *testing.T) run.Budget {
	t.Helper()
	budget := run.Budget{Steps: run.NewQuota(64), Effects: run.NewQuota(64), Messages: run.NewQuota(64)}
	return budget
}
