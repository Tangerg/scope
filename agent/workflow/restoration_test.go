package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

func TestEveryStageRestoresAtItsCommittedMessageBoundaries(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	increment := mustTransform(t, "increment", func(_ context.Context, value int) (int, error) { return value + 1, nil })
	leaf, err := Compile[int, int](compiler, Config{Name: "test.increment", Description: "Increment once.", Revision: "increment", Stages: []Stage{increment}})
	if err != nil {
		t.Fatal(err)
	}
	call, err := Call(CallConfig{ID: "call", Child: run.ChildBinding{Program: leaf.Erase()}})
	if err != nil {
		t.Fatal(err)
	}
	choose, err := Switch(SwitchConfig[int]{ID: "choose", Cases: []SwitchCase{{ID: "increment", Child: run.ChildBinding{Program: leaf.Erase()}}}, Select: func(context.Context, int) (string, error) { return "increment", nil }})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := Fork(ForkConfig[int, int, int]{ID: "fork", Branches: []ForkBranch{{ID: "a", Child: run.ChildBinding{Program: leaf.Erase()}}, {ID: "b", Child: run.ChildBinding{Program: leaf.Erase()}}, {ID: "c", Child: run.ChildBinding{Program: leaf.Erase()}}}, WindowSize: 2, Reduce: func(_ context.Context, _ int, outputs []int) (int, error) {
		sum := 0
		for _, value := range outputs {
			sum += value
		}
		return sum, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	loop, err := Loop(LoopConfig[int]{ID: "loop", Body: run.ChildBinding{Program: leaf.Erase()}, MaxIterations: run.NewQuota(3), Predicate: func(_ context.Context, value int) (bool, error) { return value >= 10, nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name     string
		stage    Stage
		expected int
	}{
		{"call", call, 3}, {"switch", choose, 3}, {"fork", fork, 9},
	} {
		t.Run(row.name, func(t *testing.T) {
			definition := mustDefinition(t, "test."+row.name, row.stage)
			result := conformancetest.Run(t, run.BindingConfig{Definition: definition, Revision: "graph", Configuration: definition.Topology()}, run.Config{}, mustPayload(t, 2))
			value, outputErr := result.Output()
			if outputErr != nil {
				t.Fatal(outputErr)
			}
			decoded, decodeErr := value.Decode[int]()
			if decodeErr != nil || decoded != row.expected {
				t.Fatal(decoded, decodeErr)
			}
		})
	}
	t.Run("loop", func(t *testing.T) {
		definition := mustDefinition(t, "test.loop", loop)
		result := conformancetest.Run(t, run.BindingConfig{Definition: definition, Revision: "loop", Configuration: definition.Topology()}, run.Config{}, mustPayload(t, 8))
		output, outputErr := result.Output()
		if outputErr != nil {
			t.Fatal(outputErr)
		}
		value, decodeErr := output.Decode[LoopResult[int]]()
		if decodeErr != nil || value.Value != 10 || value.Iterations != 2 || !value.Satisfied {
			t.Fatal(value, decodeErr)
		}
	})
	mapped, err := Map(MapConfig[int, int]{ID: "map", Child: run.ChildBinding{Program: leaf.Erase()}, MaxItems: 5, WindowSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("map", func(t *testing.T) {
		definition := mustDefinition(t, "test.map", mapped)
		result := conformancetest.Run(t, run.BindingConfig{Definition: definition, Configuration: definition.Topology()}, run.Config{}, mustPayload(t, []int{1, 2, 3}))
		output, outputErr := result.Output()
		if outputErr != nil {
			t.Fatal(outputErr)
		}
		values, decodeErr := output.Decode[[]int]()
		if decodeErr != nil || len(values) != 3 || values[0] != 2 || values[2] != 4 {
			t.Fatal(values, decodeErr)
		}
	})
}

func mustPayload(t *testing.T, value any) run.Payload {
	t.Helper()
	payload, err := run.EncodePayload(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestRestoreRejectsUnownedAndContradictoryWorkflowProgress(t *testing.T) {
	leaf := mustProgram(t, mustDefinition(t, "test.leaf", mustTransform(t, "identity", func(_ context.Context, value int) (int, error) { return value, nil })), "leaf")
	stage, err := Map(MapConfig[int, int]{ID: "map", Child: run.ChildBinding{Program: leaf}, MaxItems: 4, WindowSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	definition := mustDefinition(t, "test.map", stage)
	first := testTurn(1)
	request := first.EffectID(0)
	for _, row := range []struct {
		name  string
		state executionState
	}{
		{"past graph", executionState{StageIndex: 2, CurrentValue: mustPayload(t, []int{1})}},
		{"completed repeats output", executionState{StageIndex: 1, CurrentValue: mustPayload(t, []int{1})}},
		{"completed retains child", executionState{StageIndex: 1, CurrentValue: mustPayload(t, []int{1}), Child: &singleChild{Invocation: child.Invocation{Request: &request}}}},
		{"wrong input schema", executionState{CurrentValue: mustPayload(t, "bad")}},
		{"single child on map", executionState{CurrentValue: mustPayload(t, []int{1}), Child: &singleChild{Invocation: child.Invocation{Request: &request}}}},
		{"fanout wrong length", executionState{CurrentValue: mustPayload(t, []int{1, 2}), Fanout: &fanoutProgress{Window: []child.Invocation{{Request: &request}}}}},
		{"fanout invalid invocation", executionState{CurrentValue: mustPayload(t, []int{1}), Fanout: &fanoutProgress{Window: []child.Invocation{{}}}}},
		{"fanout repeated obligation", executionState{CurrentValue: mustPayload(t, []int{1, 2}), Fanout: &fanoutProgress{Window: []child.Invocation{{Request: &request}, {Request: &request}}}}},
		{"fanout invalid cursor", executionState{CurrentValue: mustPayload(t, []int{1, 2}), Fanout: &fanoutProgress{Completed: []run.Payload{mustPayload(t, 1)}, Window: []child.Invocation{{Request: &request}}}}},
		{"single child beside fan-out", executionState{CurrentValue: mustPayload(t, []int{1}), Child: &singleChild{Invocation: child.Invocation{Request: &request}}, Fanout: &fanoutProgress{Window: []child.Invocation{{Request: &request}}}}},
		{"fanout without a window", executionState{CurrentValue: mustPayload(t, []int{1}), Fanout: &fanoutProgress{}}},
		{"fanout finished still active", executionState{CurrentValue: mustPayload(t, []int{1}), Fanout: &fanoutProgress{Completed: []run.Payload{mustPayload(t, 1)}, Window: []child.Invocation{{Request: &request}}}}},
	} {
		t.Run(row.name, func(t *testing.T) {
			state, err := row.state.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := definition.Restore(t.Context(), state); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("state admitted or unclassified: %v", err)
			}
		})
	}
}
