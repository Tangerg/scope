package planning

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

func FuzzExecutionStateRestore(f *testing.F) {
	inputSchema, err := agent.SchemaFor[struct{}]()
	if err != nil {
		f.Fatal(err)
	}
	done, err := NewCondition("world.done", TruthTrue)
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
		MaxActionAttempts: agent.NewQuota(4),
	})
	if err != nil {
		f.Fatal(err)
	}
	input, err := agent.EncodePayload(struct{}{})
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
	f.Add([]byte(`{"phase":"completed","input":{},"world_state":{"conditions":[]}}`))
	f.Add([]byte(`{"phase":"ready_sense","input":{},"world_state":{"conditions":[]}}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		state, err := agent.ParseExecutionState(executionStateKind, payload)
		if err != nil {
			return
		}
		restored, err := definition.Restore(t.Context(), state)
		if err != nil {
			return
		}
		captured, err := restored.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		restoredAgain, err := definition.Restore(t.Context(), captured)
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
	f.Add([]byte(`{"input":{}}`))
	f.Add([]byte(`{"input":{},"action":{"name":"action.finish","world_state":{"conditions":[]}}}`))
	f.Add([]byte(`{"world_state":{"conditions":[]}}`))
	f.Add([]byte(`{"diagnostic":"sensor failed"}`))
	f.Add([]byte(`{"host_error":"action binding rejected"}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if effect, err := decodeEffect(payload); err == nil {
			encoded, err := jsonv2.Marshal(effect, jsonv2.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeEffect(encoded); err != nil {
				t.Fatalf("accepted Effect did not round trip: %v", err)
			}
		}
		if failure, err := jsonwire.Decode[settlementFailure](payload); err == nil && failure.valid() {
			encoded, err := jsonv2.Marshal(failure, jsonv2.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			if decoded, err := jsonwire.Decode[settlementFailure](encoded); err != nil || decoded != failure {
				t.Fatalf("accepted settlement failure did not round trip: %v", err)
			}
		}
	})
}

func TestPlanningSettlementFailureIsExclusive(t *testing.T) {
	for payload, valid := range map[string]bool{
		`{"host_error":"action binding rejected"}`: true,
		`{"diagnostic":"sensor failed"}`:           true,
		`{"host_error":""}`:                        false,
		`{}`:                                       false,
		`{"host_error":"invalid","diagnostic":"sensor failed"}`:    false,
		`{"host_error":"invalid","world_state":{"conditions":[]}}`: false,
	} {
		failure, err := jsonwire.Decode[settlementFailure]([]byte(payload))
		if (err == nil && failure.valid()) != valid {
			t.Fatalf("settlement failure %s valid=%t, want %t", payload, err == nil && failure.valid(), valid)
		}
	}
}

func TestDispatcherReplaysOnlyObservationEffects(t *testing.T) {
	input, err := agent.EncodePayload(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := newSenseEffect(input)
	if err != nil {
		t.Fatal(err)
	}
	done, err := NewCondition("world.done", TruthTrue)
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
	if got := dispatcher.Policy(observation).Replay; got != agent.ReplayPolicySameIdentity {
		t.Fatalf("observation replay policy = %s", got)
	}
	if got := dispatcher.Policy(actionEffect).Replay; got != agent.ReplayPolicyNever {
		t.Fatalf("Action replay policy = %s", got)
	}
	if got := dispatcher.Policy(agent.Effect{}).Replay; got != agent.ReplayPolicyNever {
		t.Fatalf("invalid Effect replay policy = %s", got)
	}
}
