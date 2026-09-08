package planning_test

import (
	"encoding/json"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/planning"
)

func TestRestoreValidatesPlanningFacts(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	action := mustAction(t, planning.ActionConfig{
		Name: "finish", Description: "Finish the pending work.", Effects: []planning.Condition{done},
	})
	definition := newManagedDefinition(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
	})
	tests := []struct {
		name    string
		payload json.RawMessage
		valid   bool
	}{
		{
			name: "successful action awaits confirmation",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},
				"planning_passes":1,"current_action_name":"finish"}`),
			valid: true,
		},
		{
			name: "failed action already recorded",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},
				"planning_passes":1,"attempts":[{"action_name":"finish","status":"failed","diagnostic":"refused"}]}`),
			valid: true,
		},
		{
			name: "unreachable completion",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]},
				"planning_passes":1,"outcome":"unreachable"}`),
			valid: true,
		},
		{
			name: "active execution has an outcome",
			payload: json.RawMessage(`{"phase":"ready_sense","input":{},"world_state":{"conditions":[]},
				"planning_passes":0,"outcome":"achieved"}`),
		},
		{
			name:    "completed execution lacks an outcome",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]},"planning_passes":0}`),
		},
		{
			name: "achieved outcome contradicts the goal",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]},
				"planning_passes":0,"outcome":"achieved"}`),
		},
		{
			name: "already achieved completion",
			payload: json.RawMessage(`{"phase":"completed","input":{},
				"world_state":{"conditions":[{"key":"world.done","truth":"true"}]},
				"planning_passes":0,"outcome":"achieved"}`),
			valid: true,
		},
		{
			name: "unreachable outcome contradicts the goal",
			payload: json.RawMessage(`{"phase":"completed","input":{},
				"world_state":{"conditions":[{"key":"world.done","truth":"true"}]},
				"planning_passes":1,"outcome":"unreachable"}`),
		},
		{
			name: "stuck outcome contradicts the goal",
			payload: json.RawMessage(`{"phase":"completed","input":{},
				"world_state":{"conditions":[{"key":"world.done","truth":"true"}]},
				"planning_passes":1,"outcome":"stuck", "attempts":[{"action_name":"finish","status":"succeeded"}]}`),
		},
		{
			name: "confirmation has no planning pass",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},
				"planning_passes":0,"current_action_name":"finish"}`),
		},
		{
			name: "current action was excluded by failure",
			payload: json.RawMessage(`{"phase":"awaiting_action","input":{},"world_state":{"conditions":[]},
				"planning_passes":2,"current_action_name":"finish",
				"attempts":[{"action_name":"finish","status":"failed","diagnostic":"refused"}]}`),
		},
		{
			name: "attempt follows exclusion",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]},
				"planning_passes":2,"outcome":"stuck","attempts":[
				{"action_name":"finish","status":"unconfirmed","diagnostic":"prediction failed"},
				{"action_name":"finish","status":"succeeded"}]}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := agent.NewExecutionState("planning", test.payload)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := definition.Restore(state)
			if !test.valid {
				if !errors.Is(err, planning.ErrInvalidExecutionState) {
					t.Fatalf("Restore error=%v, want ErrInvalidExecutionState", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restored.Snapshot(); err != nil {
				t.Fatalf("Snapshot after Restore: %v", err)
			}
		})
	}
}
