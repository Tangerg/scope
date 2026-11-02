package workflow

import (
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

type switchInput struct {
	Case  string `json:"case"`
	Value int    `json:"value"`
}

func TestSwitchRunsOnlyTheSelectedManagedChild(t *testing.T) {
	left := mustProgram(t, mustDefinition(t, "test.switch_left",
		mustTransform(t, "left", func(_ context.Context, input switchInput) (numberOutput, error) {
			return numberOutput{Value: input.Value + 10}, nil
		}),
	), "switch-left")
	right := mustProgram(t, mustDefinition(t, "test.switch_right",
		mustTransform(t, "right", func(_ context.Context, input switchInput) (numberOutput, error) {
			return numberOutput{Value: input.Value + 20}, nil
		}),
	), "switch-right")
	stage, err := Switch(SwitchConfig[switchInput]{
		ID:     "choose",
		Select: func(_ context.Context, input switchInput) (string, error) { return input.Case, nil },
		Cases: []SwitchCase{
			{ID: "left", Program: left, Budget: mustBudget(t)},
			{ID: "right", Program: right, Budget: mustBudget(t)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stage.Valid() {
		t.Fatalf("Switch Stage = %#v", stage)
	}
	program := mustProgram(t, mustDefinition(t, "test.switch", stage), "switch")
	for _, test := range []struct {
		selected string
		want     int
	}{{selected: "left", want: 13}, {selected: "right", want: 23}} {
		t.Run(test.selected, func(t *testing.T) {
			store := run.NewMemoryStore()
			runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: store})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := run.EncodePayload(switchInput{Case: test.selected, Value: 3})
			result, err := runtest.Observe(context.Background(), runtime, program, input)
			if err != nil {
				t.Fatal(err)
			}
			output := decodeCompleted[numberOutput](t, result)
			if output.Value != test.want {
				t.Fatalf("Switch output = %#v", output)
			}
			tree, _, err := store.Load(t.Context(), result.ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			wantChild := left.ProgramRef()
			if test.selected == "right" {
				wantChild = right.ProgramRef()
			}
			var children []run.Started
			for _, entry := range tree.Entries {
				if started, ok := entry.Record.(run.Started); ok && started.Process.Depth() > 0 {
					children = append(children, started)
				}
			}
			if len(children) != 1 || children[0].Program != wantChild || children[0].Process.Depth() != 1 {
				t.Fatalf("Switch did not start exactly the selected child: %+v", children)
			}
			if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSwitchRejectsUndeclaredSelection(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "test.switch_known",
		mustTransform(t, "known", func(_ context.Context, input switchInput) (numberOutput, error) {
			return numberOutput{Value: input.Value}, nil
		}),
	), "switch-known")
	stage, err := Switch(SwitchConfig[switchInput]{
		ID: "choose", Select: func(context.Context, switchInput) (string, error) { return "missing", nil },
		Cases: []SwitchCase{{ID: "known", Program: child, Budget: mustBudget(t)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	program := mustProgram(t, mustDefinition(t, "test.switch_unknown", stage), "switch-unknown")
	runtime, _ := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	input, _ := run.EncodePayload(switchInput{Case: "missing", Value: 1})
	result, err := runtest.Observe(context.Background(), runtime, program, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Termination().Status() != run.StatusFailed || !present || failure.Code() != "workflow.switch.case_unknown" {
		t.Fatalf("Switch termination = %#v", result.Termination())
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchRequiresMatchingUniqueCases(t *testing.T) {
	valid := mustProgram(t, mustDefinition(t, "test.switch_valid",
		mustTransform(t, "valid", func(_ context.Context, input switchInput) (numberOutput, error) {
			return numberOutput{Value: input.Value}, nil
		}),
	), "switch-valid")
	wrongInput := mustProgram(t, mustDefinition(t, "test.switch_wrong_input",
		mustTransform(t, "wrong", func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput(input), nil
		}),
	), "switch-wrong-input")
	wrongOutput := mustProgram(t, mustDefinition(t, "test.switch_wrong_output",
		mustTransform(t, "wrong", func(_ context.Context, input switchInput) (string, error) {
			return input.Case, nil
		}),
	), "switch-wrong-output")
	for name, cases := range map[string][]SwitchCase{
		"empty":                 nil,
		"missing program":       {{ID: "missing"}},
		"invalid case identity": {{ID: "invalid name", Program: valid}},
		"duplicate": {
			{ID: "same", Program: valid, Budget: mustBudget(t)},
			{ID: "same", Program: valid, Budget: mustBudget(t)},
		},
		"input mismatch": {{ID: "wrong", Program: wrongInput, Budget: mustBudget(t)}},
		"output mismatch": {
			{ID: "valid", Program: valid},
			{ID: "wrong", Program: wrongOutput},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Switch(SwitchConfig[switchInput]{
				ID: "choose", Select: func(context.Context, switchInput) (string, error) { return "same", nil }, Cases: cases,
			})
			if !errors.Is(err, ErrInvalidStage) {
				t.Fatalf("Switch error = %v", err)
			}
		})
	}
}

func decodeCompleted[O any](t *testing.T, result run.Result[run.Payload]) O {
	t.Helper()
	if result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("Process status = %s, termination = %#v", result.Termination().Status(), result.Termination())
	}
	output, present := result.Termination().Output()
	if !present {
		t.Fatal("completed Process has no Output")
	}
	decoded, err := output.Decode[O]()
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
