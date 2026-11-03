package workflow

import (
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestMapUsesManagedChildrenAndPreservesItemOrder(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "test.map_child",
		mustTransform(t, "double", func(_ context.Context, input forkInput) (numberOutput, error) {
			return numberOutput{Value: input.Value * 2}, nil
		}),
	), "map-child")
	stage, err := Map(MapConfig[forkInput, numberOutput]{
		ID: "items", Child: run.ChildBinding{Program: child, Budget: mustBudget(t)},
		WindowSize: 2, MaxItems: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stage.Valid() {
		t.Fatalf("Map Stage = %#v", stage)
	}
	program := mustProgram(t, mustDefinition(t, "test.map", stage), "map")
	runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := run.EncodePayload([]forkInput{{Value: 3}, {Value: 1}, {Value: 2}})
	result, err := runtest.Observe(context.Background(), runtime, program, input)
	if err != nil {
		t.Fatal(err)
	}
	output := decodeCompleted[[]numberOutput](t, result)
	if !slices.Equal(output, []numberOutput{{Value: 6}, {Value: 2}, {Value: 4}}) {
		t.Fatalf("Map output = %#v", output)
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestMapEmptyInputProducesNonNilEmptyOutput(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "test.empty_map_child",
		mustTransform(t, "identity", func(_ context.Context, input forkInput) (numberOutput, error) {
			return numberOutput(input), nil
		}),
	), "empty-map-child")
	stage, err := Map(MapConfig[forkInput, numberOutput]{
		ID: "items", Child: run.ChildBinding{Program: child, Budget: mustBudget(t)},
		WindowSize: 1, MaxItems: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	program := mustProgram(t, mustDefinition(t, "test.empty_map", stage), "empty-map")
	runtime, _ := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	input, _ := run.EncodePayload([]forkInput{})
	result, err := runtest.Observe(context.Background(), runtime, program, input)
	if err != nil {
		t.Fatal(err)
	}
	output := decodeCompleted[[]numberOutput](t, result)
	if output == nil || len(output) != 0 {
		t.Fatalf("empty Map output = %#v", output)
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestMapRejectsInputAboveMaxItemsBeforeStartingChildren(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "test.limited_map_child",
		mustTransform(t, "identity", func(_ context.Context, input forkInput) (numberOutput, error) {
			return numberOutput(input), nil
		}),
	), "limited-map-child")
	stage, err := Map(MapConfig[forkInput, numberOutput]{
		ID: "items", Child: run.ChildBinding{Program: child, Budget: mustBudget(t)},
		WindowSize: 2, MaxItems: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	program := mustProgram(t, mustDefinition(t, "test.limited_map", stage), "limited-map")
	runtime, _ := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	input, _ := run.EncodePayload([]forkInput{{Value: 1}, {Value: 2}, {Value: 3}})
	result, err := runtest.Observe(context.Background(), runtime, program, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Termination().Status() != run.StatusFailed || !present || failure.Code() != "workflow.map.max_items_exceeded" {
		t.Fatalf("Map termination = %#v", result.Termination())
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestMapRequiresExplicitLimitsAndMatchingChildContract(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "test.valid_map_child",
		mustTransform(t, "identity", func(_ context.Context, input forkInput) (numberOutput, error) {
			return numberOutput(input), nil
		}),
	), "valid-map-child")
	wrong := mustProgram(t, mustDefinition(t, "test.wrong_map_child",
		mustTransform(t, "wrong", func(context.Context, textValue) (numberOutput, error) {
			return numberOutput{}, nil
		}),
	), "wrong-map-child")
	valid := MapConfig[forkInput, numberOutput]{
		ID: "items", Child: run.ChildBinding{Budget: mustBudget(t)},
	}
	for name, config := range map[string]MapConfig[forkInput, numberOutput]{
		"zero window size": {ID: valid.ID, Child: run.ChildBinding{Program: child, Budget: valid.Child.Budget}, MaxItems: 2},
		"zero max items":   {ID: valid.ID, Child: run.ChildBinding{Program: child, Budget: valid.Child.Budget}, WindowSize: 1},
		"oversized window": {
			ID: valid.ID, Child: run.ChildBinding{Program: child, Budget: valid.Child.Budget}, WindowSize: 3, MaxItems: 2,
		},
		"schema mismatch": {
			ID: valid.ID, Child: run.ChildBinding{Program: wrong, Budget: valid.Child.Budget}, WindowSize: 1, MaxItems: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Map(config); !errors.Is(err, ErrInvalidStage) {
				t.Fatalf("Map error = %v", err)
			}
		})
	}
}
