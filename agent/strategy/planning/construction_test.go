package planning_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/planning"
	"github.com/Tangerg/scope/agent/strategy/planning/goap"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestPlanningConstructionRejectsAmbiguousAndUnusableFacts(t *testing.T) {
	done := mustCondition(t, "world.done", planning.TruthTrue)
	for _, conditions := range [][]planning.Condition{{{}}, {done, done}} {
		if _, err := planning.NewWorldState(conditions...); !errors.Is(err, planning.ErrInvalidWorldState) {
			t.Fatalf("invalid observed facts were accepted: %v", err)
		}
		if _, err := planning.NewGoal(planning.GoalConfig{Name: "goal", Description: "Reach a goal.", Conditions: conditions}); !errors.Is(err, planning.ErrInvalidGoal) {
			t.Fatalf("invalid goal facts were accepted: %v", err)
		}
		if _, err := planning.NewAction(planning.ActionConfig{Name: "action", Description: "Change a fact.", Preconditions: conditions, Effects: []planning.Condition{done}}); !errors.Is(err, planning.ErrInvalidAction) {
			t.Fatalf("invalid action preconditions were accepted: %v", err)
		}
		if _, err := planning.NewAction(planning.ActionConfig{Name: "action", Description: "Change a fact.", Effects: conditions}); !errors.Is(err, planning.ErrInvalidAction) {
			t.Fatalf("invalid action predictions were accepted: %v", err)
		}
	}
	for _, config := range []planning.GoalConfig{
		{Name: "invalid name", Description: "Reach a goal.", Conditions: []planning.Condition{done}},
		{Name: "goal", Conditions: []planning.Condition{done}},
		{Name: "goal", Description: "Reach a goal."},
	} {
		if goal, err := planning.NewGoal(config); goal.Valid() || !errors.Is(err, planning.ErrInvalidGoal) {
			t.Fatalf("invalid goal = %v, %v", goal, err)
		}
	}
	for _, config := range []planning.ActionConfig{
		{Name: "invalid name", Description: "Change a fact.", Effects: []planning.Condition{done}},
		{Name: "action", Effects: []planning.Condition{done}},
		{Name: "action", Description: "Change a fact."},
	} {
		if action, err := planning.NewAction(config); action.Valid() || !errors.Is(err, planning.ErrInvalidAction) {
			t.Fatalf("invalid action = %v, %v", action, err)
		}
	}
	if _, err := (planning.Action{}).Apply(planning.WorldState{}); !errors.Is(err, planning.ErrInvalidAction) {
		t.Fatalf("unconfigured action changed state: %v", err)
	}
	if _, err := (planning.Action{}).Cost(planning.WorldState{}); !errors.Is(err, planning.ErrInvalidActionCost) {
		t.Fatalf("unconfigured action acquired a default cost: %v", err)
	}
}

func TestPlanningProblemRequiresOneOwnerForEveryActionName(t *testing.T) {
	done := mustCondition(t, "world.done", planning.TruthTrue)
	goal := mustGoal(t, done)
	action := mustAction(t, planning.ActionConfig{Name: "finish", Description: "Finish work.", Effects: []planning.Condition{done}})
	for _, actions := range [][]planning.Action{{{}}, {action, action}} {
		if problem, err := planning.NewProblem(planning.WorldState{}, goal, actions...); problem.Valid() || !errors.Is(err, planning.ErrInvalidProblem) {
			t.Fatalf("unusable or ambiguous action ownership = %v, %v", problem, err)
		}
	}
	if problem, err := planning.NewProblem(planning.WorldState{}, planning.Goal{}, action); problem.Valid() || !errors.Is(err, planning.ErrInvalidProblem) {
		t.Fatalf("problem without a goal = %v, %v", problem, err)
	}
}

func TestActionBindingsRejectMissingExecutionContracts(t *testing.T) {
	done := mustCondition(t, "world.done", planning.TruthTrue)
	action := mustAction(t, planning.ActionConfig{Name: "finish", Description: "Finish work.", Effects: []planning.Condition{done}})
	for _, config := range []planning.DispatcherBindingConfig{
		{},
		{Action: action, RequiredCapabilities: []agent.Capability{{}}},
	} {
		if binding, err := planning.NewDispatcherBinding(config); binding.Valid() || !errors.Is(err, planning.ErrInvalidAction) {
			t.Fatalf("unusable dispatcher binding = %v, %v", binding, err)
		}
	}
	if binding, err := planning.NewChildBinding(planning.ChildBindingConfig{Action: action}); binding.Valid() || !errors.Is(err, planning.ErrInvalidAction) {
		t.Fatalf("child binding without an exact deployment = %v, %v", binding, err)
	}
	binding := mustDispatcherBinding(t, action)
	if binding.Action().Name() != action.Name() || binding.Action().Description() != action.Description() {
		t.Fatal("execution binding changed the predictive action contract")
	}
}

func TestPlanningDefinitionRequiresAnExactExecutableContract(t *testing.T) {
	done := mustCondition(t, "world.done", planning.TruthTrue)
	action := mustAction(t, planning.ActionConfig{Name: "finish", Description: "Finish work.", Effects: []planning.Condition{done}})
	binding := mustDispatcherBinding(t, action)
	schema, err := jsonschema.For[struct{}]()
	if err != nil {
		t.Fatal(err)
	}
	base := planning.DefinitionConfig{
		Name: "planning", Description: "Plan work.", InputSchema: schema, Goal: mustGoal(t, done),
		Actions: []planning.ActionBinding{binding}, Planner: goap.New(goap.Config{}),
	}
	for _, change := range []func(*planning.DefinitionConfig){
		func(config *planning.DefinitionConfig) { config.InputSchema = jsonschema.Schema{} },
		func(config *planning.DefinitionConfig) { config.Goal = planning.Goal{} },
		func(config *planning.DefinitionConfig) { config.Planner = nil },
		func(config *planning.DefinitionConfig) { config.Actions = []planning.ActionBinding{{}} },
		func(config *planning.DefinitionConfig) { config.Actions = []planning.ActionBinding{binding, binding} },
		func(config *planning.DefinitionConfig) { config.Name = "invalid name" },
	} {
		config := base
		change(&config)
		if definition, definitionErr := planning.NewDefinition(config); definition != nil || !errors.Is(definitionErr, planning.ErrInvalidDefinitionConfig) {
			t.Fatalf("invalid definition = %v, %v", definition, definitionErr)
		}
	}
	for _, definition := range []*planning.Definition{nil, {}} {
		if definition.Descriptor().Valid() || len(definition.ChildDeployments()) != 0 {
			t.Fatal("unconfigured planning exposed a usable contract")
		}
		if execution, startErr := definition.Start(agent.Payload{}); execution != nil || !errors.Is(startErr, planning.ErrInvalidDefinitionConfig) {
			t.Fatalf("unconfigured Start = %v, %v", execution, startErr)
		}
		if execution, restoreErr := definition.Restore(t.Context(), agent.ExecutionState{}); execution != nil || !errors.Is(restoreErr, planning.ErrInvalidDefinitionConfig) {
			t.Fatalf("unconfigured Restore = %v, %v", execution, restoreErr)
		}
	}
	definition, err := planning.NewDefinition(base)
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodePayload("outside the input contract")
	if err != nil {
		t.Fatal(err)
	}
	if execution, err := definition.Start(input); execution != nil || !errors.Is(err, agent.ErrInvalidPayload) {
		t.Fatalf("invalid input = %v, %v", execution, err)
	}
	executor := planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
		return planning.ActionSucceeded(), nil
	})
	if _, err := planning.NewDispatcher(definition, planning.DispatcherConfig{
		Sensor: newManagedWorld(t), ActionExecutors: map[string]planning.ActionExecutor{action.Name(): executor, "invalid name": executor},
	}); !errors.Is(err, planning.ErrInvalidDispatcherConfig) {
		t.Fatalf("invalid executor identity was admitted: %v", err)
	}
}
