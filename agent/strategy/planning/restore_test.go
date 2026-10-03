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
	done := mustCondition(t, "world.done", planning.TruthTrue)
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
			name:    "stored planning passes",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},"planning_passes":0}`),
		},
		{
			name:    "missing observed world",
			payload: json.RawMessage(`{"phase":"awaiting_action","input":{},"current_action_name":"finish"}`),
		},
		{
			name:    "null observed world",
			payload: json.RawMessage(`{"phase":"awaiting_action","input":{},"world_state":null,"current_action_name":"finish"}`),
		},
		{
			name:    "successful action awaits confirmation",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},"current_action_name":"finish"}`),
			valid:   true,
		},
		{
			name: "failed action already recorded",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},
				"attempts":[{"action_name":"finish","status":"failed","diagnostic":"refused"}]}`),
			valid: true,
		},
		{
			name:    "unreachable completion",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]}}`),
			valid:   true,
		},
		{
			name: "unknown state member",
			payload: json.RawMessage(`{"phase":"ready_sense","input":{},"world_state":{"conditions":[]},
				"unexpected":true}`),
		},
		{
			name: "already achieved completion",
			payload: json.RawMessage(`{"phase":"completed","input":{},
				"world_state":{"conditions":[{"key":"world.done","truth":"true"}]}}`),
			valid: true,
		},
		{
			name: "achieved completion after an attempt",
			payload: json.RawMessage(`{"phase":"completed","input":{},
				"world_state":{"conditions":[{"key":"world.done","truth":"true"}]},
				"attempts":[{"action_name":"finish","status":"succeeded"}]}`),
			valid: true,
		},
		{
			name: "stuck completion after an excluded attempt",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]},
				"attempts":[{"action_name":"finish","status":"failed","diagnostic":"refused"}]}`),
			valid: true,
		},
		{
			name: "current action was excluded by failure",
			payload: json.RawMessage(`{"phase":"awaiting_action","input":{},"world_state":{"conditions":[]},
				"current_action_name":"finish",
				"attempts":[{"action_name":"finish","status":"failed","diagnostic":"refused"}]}`),
		},
		{
			name: "attempt follows exclusion",
			payload: json.RawMessage(`{"phase":"completed","input":{},"world_state":{"conditions":[]},
				"attempts":[
				{"action_name":"finish","status":"unconfirmed","diagnostic":"prediction failed"},
				{"action_name":"finish","status":"succeeded"}]}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := agent.ParseExecutionState("planning", test.payload)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := definition.Restore(t.Context(), state)
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
	done := mustCondition(t, "world.done", planning.TruthTrue)
	action := mustAction(t, planning.ActionConfig{
		Name: "finish", Description: "Finish the pending work.", Effects: []planning.Condition{done},
	})
	definition := newManagedDefinition(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
		maxActionAttempts: agent.NewQuota(1),
	})
	tests := []struct {
		name    string
		payload json.RawMessage
		valid   bool
	}{
		{
			name:    "last permitted action is pending",
			payload: json.RawMessage(`{"phase":"awaiting_action","input":{},"world_state":{"conditions":[]},"current_action_name":"finish"}`),
			valid:   true,
		},
		{
			name:    "last permitted action awaits confirmation",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},"current_action_name":"finish"}`),
			valid:   true,
		},
		{
			name: "settled last action still permits sensing",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},
				"attempts":[{"action_name":"finish","status":"failed","diagnostic":"refused"}]}`),
			valid: true,
		},
		{
			name: "pending action exceeds limit",
			payload: json.RawMessage(`{"phase":"awaiting_action","input":{},"world_state":{"conditions":[]},
				"current_action_name":"finish",
				"attempts":[{"action_name":"finish","status":"succeeded"}]}`),
		},
		{
			name: "unconfirmed action exceeds limit",
			payload: json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]},
				"current_action_name":"finish",
				"attempts":[{"action_name":"finish","status":"succeeded"}]}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := agent.ParseExecutionState("planning", test.payload)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := definition.Restore(t.Context(), state)
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
	done := mustCondition(t, "world.done", planning.TruthTrue)
	action := mustAction(t, planning.ActionConfig{
		Name: "finish", Description: "Finish pending work.", Effects: []planning.Condition{done},
	})
	definition := newManagedDefinition(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
	})
	state, err := agent.ParseExecutionState("planning", json.RawMessage(`{"phase":"awaiting_sense","input":{},"world_state":{"conditions":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Restore(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if decodeErr := jsonv2.Unmarshal([]byte(`{"id":"signal:engine:sense","payload":{"unknown":true}}`), &signal); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	_, err = execution.Step(t.Context(), []agent.Signal{signal})
	if !errors.Is(err, planning.ErrInvalidProtocol) || !errors.Is(err, jsonv2.ErrUnknownName) {
		t.Fatalf("Step error = %v, want protocol and unknown member causes", err)
	}
}

func TestRestoreKeepsSingleChildProgressWithinItsAction(t *testing.T) {
	done := mustCondition(t, "world.done", planning.TruthTrue)
	action := mustAction(t, planning.ActionConfig{
		Name: "finish", Description: "Complete the work.", Effects: []planning.Condition{done},
	})
	world := newManagedWorld(t)
	child := newManagedDeployment(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
		executors: map[string]planning.ActionExecutor{"finish": world.apply(action)}, sensor: world,
	})
	binding, err := planning.NewChildBinding(planning.ChildBindingConfig{
		Action: action, Deployment: child, Budget: agent.Budget{Steps: agent.NewQuota(32), Effects: agent.NewQuota(32), Signals: agent.NewQuota(64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := newManagedDefinition(t, managedDeploymentConfig{goal: mustGoal(t, done), bindings: []planning.ActionBinding{binding}})
	for _, test := range []struct {
		phase string
		child json.RawMessage
		valid bool
	}{
		{"awaiting_action", json.RawMessage(`{}`), true},
		{"awaiting_action", json.RawMessage(`{"process_id":"child"}`), true},
		{"awaiting_action", json.RawMessage(`{"process_id":"child","wait_id":"wait"}`), true},
		{"awaiting_action", json.RawMessage(`null`), false},
		{"awaiting_action", json.RawMessage(`{"wait_id":"wait"}`), false},
		{"awaiting_action", json.RawMessage(`{"process_id":"child","unknown":true}`), false},
		{"awaiting_sense", json.RawMessage(`{}`), false},
		{"child", json.RawMessage(`{}`), false},
	} {
		payload, encodeErr := jsonv2.Marshal(struct {
			Phase      string          `json:"phase"`
			Input      json.RawMessage `json:"input"`
			WorldState json.RawMessage `json:"world_state"`
			Action     string          `json:"current_action_name"`
			Child      json.RawMessage `json:"child"`
		}{test.phase, json.RawMessage(`{}`), json.RawMessage(`{"conditions":[]}`), "finish", test.child})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		state, stateErr := agent.ParseExecutionState("planning", payload)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		restored, restoreErr := definition.Restore(t.Context(), state)
		if !test.valid {
			if !errors.Is(restoreErr, planning.ErrInvalidExecutionState) {
				t.Fatalf("Restore(%s) error=%v", payload, restoreErr)
			}
			continue
		}
		if restoreErr != nil {
			t.Fatalf("Restore(%s): %v", payload, restoreErr)
		}
		snapshot, snapshotErr := restored.Snapshot()
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		if _, restoreAgainErr := definition.Restore(t.Context(), snapshot); restoreAgainErr != nil {
			t.Fatal(restoreAgainErr)
		}
	}
}
