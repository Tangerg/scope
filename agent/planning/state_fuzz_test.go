package planning

import (
	"bytes"
	"context"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func FuzzExecutionStateRestore(f *testing.F) {
	inputSchema, err := agent.SchemaFor[struct{}]()
	if err != nil {
		f.Fatal(err)
	}
	done, err := NewCondition("world.done", True)
	if err != nil {
		f.Fatal(err)
	}
	goal, err := NewGoal(GoalConfig{
		Name: "goal.done", Description: "Reach the completed world state.", Conditions: []Condition{done},
	})
	if err != nil {
		f.Fatal(err)
	}
	action, err := NewAction(ActionConfig{
		Name: "action.finish", Description: "Finish the pending work.", Effects: []Condition{done},
	})
	if err != nil {
		f.Fatal(err)
	}
	binding, err := NewDispatcherBinding(DispatcherBindingConfig{Action: action})
	if err != nil {
		f.Fatal(err)
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "planning.fuzz", Description: "Validate restored Planning state.",
		InputSchema: inputSchema, Goal: goal, Actions: []ActionBinding{binding},
		Planner: PlannerFunc(func(context.Context, Problem) (Plan, bool, error) {
			return Plan{}, false, nil
		}),
		MaxActionAttempts: 4,
	})
	if err != nil {
		f.Fatal(err)
	}
	input, err := agent.EncodeInput(struct{}{})
	if err != nil {
		f.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		f.Fatal(err)
	}
	initial, err := execution.Snapshot()
	if err != nil {
		f.Fatal(err)
	}
	if _, stepErr := execution.Step(context.Background(), nil); stepErr != nil {
		f.Fatal(stepErr)
	}
	awaiting, err := execution.Snapshot()
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte(initial.Payload()))
	f.Add([]byte(awaiting.Payload()))
	f.Add([]byte(`{"phase":"completed","input":{},"world_state":{"conditions":[]},"planning_passes":1}`))
	f.Add([]byte(`{"phase":"ready_sense","input":{},"world_state":{"conditions":[]},"planning_passes":0,"excluded_action_names":["action.finish"]}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		state, err := agent.NewExecutionState(executionStateKind, payload)
		if err != nil {
			return
		}
		restored, err := definition.Restore(state)
		if err != nil {
			return
		}
		captured, err := restored.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		restoredAgain, err := definition.Restore(captured)
		if err != nil {
			t.Fatal(err)
		}
		recaptured, err := restoredAgain.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(captured.Payload(), recaptured.Payload()) {
			t.Fatalf("second restoration changed state\nfirst:  %s\nsecond: %s", captured.Payload(), recaptured.Payload())
		}
	})
}

func FuzzPlanningProtocol(f *testing.F) {
	f.Add([]byte(`{"operation":"sense","input":{}}`))
	f.Add([]byte(`{"operation":"action","input":{},"action":{"name":"action.finish","description":"Finish work.","world_state":{"conditions":[]}}}`))
	f.Add([]byte(`{"operation":"sense","sensing":{"world_state":{"conditions":[]}}}`))
	f.Add([]byte(`{"operation":"action","action":{"succeeded":true}}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if effect, err := decodeEffect(payload); err == nil {
			encoded, err := encodeProtocol(effect)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeEffect(encoded); err != nil {
				t.Fatalf("accepted Effect did not round trip: %v", err)
			}
		}
		if signal, err := decodeSignal(payload); err == nil {
			encoded, err := encodeProtocol(signal)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeSignal(encoded); err != nil {
				t.Fatalf("accepted Signal did not round trip: %v", err)
			}
		}
	})
}

func TestDispatcherReplaysOnlyObservationEffects(t *testing.T) {
	input, err := agent.EncodeInput(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := newSenseEffect(input)
	if err != nil {
		t.Fatal(err)
	}
	done, err := NewCondition("world.done", True)
	if err != nil {
		t.Fatal(err)
	}
	action, err := NewAction(ActionConfig{
		Name: "action.finish", Description: "Finish the pending work.", Effects: []Condition{done},
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewDispatcherBinding(DispatcherBindingConfig{Action: action})
	if err != nil {
		t.Fatal(err)
	}
	actionEffect, err := newActionEffect(input, binding, WorldState{})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{}
	if got := dispatcher.ReplayPolicy(observation); got != agent.ReplayPolicySameIdentity {
		t.Fatalf("observation replay policy = %s", got)
	}
	if got := dispatcher.ReplayPolicy(actionEffect); got != agent.ReplayPolicyNever {
		t.Fatalf("Action replay policy = %s", got)
	}
	if got := dispatcher.ReplayPolicy(agent.Effect{}); got != agent.ReplayPolicyNever {
		t.Fatalf("invalid Effect replay policy = %s", got)
	}
}
