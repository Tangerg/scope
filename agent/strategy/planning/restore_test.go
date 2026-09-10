package planning_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/planning"
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
				"planning_passes":1}`),
			valid: true,
		},
		{
			name: "unknown state member",
			payload: json.RawMessage(`{"phase":"ready_sense","input":{},"world_state":{"conditions":[]},
				"planning_passes":0,"unexpected":true}`),
		},
		{
			name:    "unreachable completion requires a planning pass",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]},"planning_passes":0}`),
		},
		{
			name: "unreachable completion has excess planning passes",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]},
				"planning_passes":2}`),
		},
		{
			name: "already achieved completion",
			payload: json.RawMessage(`{"phase":"completed","input":{},
				"world_state":{"conditions":[{"key":"world.done","truth":"true"}]},
				"planning_passes":0}`),
			valid: true,
		},
		{
			name: "achieved completion has an unaccounted planning pass",
			payload: json.RawMessage(`{"phase":"completed","input":{},
				"world_state":{"conditions":[{"key":"world.done","truth":"true"}]},
				"planning_passes":1}`),
		},
		{
			name: "achieved completion after an attempt",
			payload: json.RawMessage(`{"phase":"completed","input":{},
				"world_state":{"conditions":[{"key":"world.done","truth":"true"}]},
				"planning_passes":1,"attempts":[{"action_name":"finish","status":"succeeded"}]}`),
			valid: true,
		},
		{
			name: "stuck completion after an excluded attempt",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]},
				"planning_passes":2,"attempts":[{"action_name":"finish","status":"failed","diagnostic":"refused"}]}`),
			valid: true,
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
				"planning_passes":2,"attempts":[
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

func TestRestoreCountsPendingActionTowardAttemptLimit(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	action := mustAction(t, planning.ActionConfig{
		Name: "finish", Description: "Finish the pending work.", Effects: []planning.Condition{done},
	})
	definition := newManagedDefinition(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
		maxActionAttempts: 1,
	})
	tests := []struct {
		name    string
		payload json.RawMessage
		valid   bool
	}{
		{
			name: "last permitted action is pending",
			payload: json.RawMessage(`{"phase":"awaiting_action","input":{},"world_state":{"conditions":[]},
				"planning_passes":1,"current_action_name":"finish"}`),
			valid: true,
		},
		{
			name: "last permitted action awaits confirmation",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},
				"planning_passes":1,"current_action_name":"finish"}`),
			valid: true,
		},
		{
			name: "settled last action still permits sensing",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},
				"planning_passes":1,"attempts":[{"action_name":"finish","status":"failed","diagnostic":"refused"}]}`),
			valid: true,
		},
		{
			name: "pending action exceeds limit",
			payload: json.RawMessage(`{"phase":"awaiting_action","input":{},"world_state":{"conditions":[]},
				"planning_passes":2,"current_action_name":"finish",
				"attempts":[{"action_name":"finish","status":"succeeded"}]}`),
		},
		{
			name: "unconfirmed action exceeds limit",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},
				"planning_passes":2,"current_action_name":"finish",
				"attempts":[{"action_name":"finish","status":"succeeded"}]}`),
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
					t.Fatalf("Restore admitted an Action beyond MaxActionAttempts: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restored.Snapshot(); err != nil {
				t.Fatalf("valid budget boundary cannot be captured: %v", err)
			}
		})
	}
}

func TestExecutionPreservesSignalDecodeCause(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	action := mustAction(t, planning.ActionConfig{
		Name: "finish", Description: "Finish pending work.", Effects: []planning.Condition{done},
	})
	definition := newManagedDefinition(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
	})
	state, err := agent.NewExecutionState("planning", json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},"planning_passes":0}`))
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Restore(state)
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if decodeErr := json.Unmarshal([]byte(`{"id":"signal","payload":{"unknown":true}}`), &signal); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	_, err = execution.Step(t.Context(), []agent.Signal{signal})
	if !errors.Is(err, planning.ErrInvalidProtocol) || !errors.Is(err, jsonv2.ErrUnknownName) {
		t.Fatalf("Step error = %v, want protocol and unknown member causes", err)
	}
}
