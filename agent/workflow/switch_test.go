package workflow_test

import (
	"context"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

type switchInput struct {
	Case  string `json:"case"`
	Value int    `json:"value"`
}

func TestSwitchRunsOnlyTheSelectedManagedChild(t *testing.T) {
	left := mustDeployment(t, mustDefinition(t, "test.workflow.switch_left",
		mustTransform(t, "left", func(_ context.Context, input switchInput) (numberOutput, error) {
			return numberOutput{Value: input.Value + 10}, nil
		}),
	), "switch-left")
	right := mustDeployment(t, mustDefinition(t, "test.workflow.switch_right",
		mustTransform(t, "right", func(_ context.Context, input switchInput) (numberOutput, error) {
			return numberOutput{Value: input.Value + 20}, nil
		}),
	), "switch-right")
	stage, err := workflow.Switch(workflow.SwitchConfig[switchInput]{
		ID:     "choose",
		Select: func(_ context.Context, input switchInput) (string, error) { return input.Case, nil },
		Cases: []workflow.SwitchCase{
			{ID: "left", Deployment: left, Budget: mustBudget(t)},
			{ID: "right", Deployment: right, Budget: mustBudget(t)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stage.Valid() {
		t.Fatalf("Switch Stage = %#v", stage)
	}
	deployment := mustDeployment(t, mustDefinition(t, "test.workflow.switch", stage), "switch")
	resolver := deploymentResolver{left.DeploymentRef(): left, right.DeploymentRef(): right}
	for _, test := range []struct {
		selected string
		want     int
	}{{selected: "left", want: 13}, {selected: "right", want: 23}} {
		t.Run(test.selected, func(t *testing.T) {
			engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := agent.EncodeInput(switchInput{Case: test.selected, Value: 3})
			result, err := engine.Run(context.Background(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			output := decodeCompleted[numberOutput](t, result)
			if output.Value != test.want {
				t.Fatalf("Switch output = %#v", output)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSwitchRejectsUndeclaredSelection(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.switch_known",
		mustTransform(t, "known", func(_ context.Context, input switchInput) (numberOutput, error) {
			return numberOutput{Value: input.Value}, nil
		}),
	), "switch-known")
	stage, err := workflow.Switch(workflow.SwitchConfig[switchInput]{
		ID: "choose", Select: func(context.Context, switchInput) (string, error) { return "missing", nil },
		Cases: []workflow.SwitchCase{{ID: "known", Deployment: child, Budget: mustBudget(t)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := mustDeployment(t, mustDefinition(t, "test.workflow.switch_unknown", stage), "switch-unknown")
	engine, _ := agent.NewEngine(agent.EngineConfig{})
	input, _ := agent.EncodeInput(switchInput{Case: "missing", Value: 1})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Status() != agent.StatusFailed || !present || failure.Code() != "workflow.switch.case_unknown" {
		t.Fatalf("Switch termination = %#v", result.Termination())
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchRequiresMatchingUniqueCases(t *testing.T) {
	valid := mustDeployment(t, mustDefinition(t, "test.workflow.switch_valid",
		mustTransform(t, "valid", func(_ context.Context, input switchInput) (numberOutput, error) {
			return numberOutput{Value: input.Value}, nil
		}),
	), "switch-valid")
	wrongInput := mustDeployment(t, mustDefinition(t, "test.workflow.switch_wrong_input",
		mustTransform(t, "wrong", func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput(input), nil
		}),
	), "switch-wrong-input")
	for name, cases := range map[string][]workflow.SwitchCase{
		"empty": nil,
		"duplicate": {
			{ID: "same", Deployment: valid, Budget: mustBudget(t)},
			{ID: "same", Deployment: valid, Budget: mustBudget(t)},
		},
		"input mismatch": {{ID: "wrong", Deployment: wrongInput, Budget: mustBudget(t)}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := workflow.Switch(workflow.SwitchConfig[switchInput]{
				ID: "choose", Select: func(context.Context, switchInput) (string, error) { return "same", nil }, Cases: cases,
			})
			if !errors.Is(err, workflow.ErrInvalidStage) {
				t.Fatalf("Switch error = %v", err)
			}
		})
	}
}

func decodeCompleted[O any](t *testing.T, result agent.Result) O {
	t.Helper()
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("Process status = %s, termination = %#v", result.Status(), result.Termination())
	}
	output, present := result.Output()
	if !present {
		t.Fatal("completed Process has no Output")
	}
	decoded, err := output.Decode[O]()
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
