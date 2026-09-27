package trajectory_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestToolSequenceKeepsPhysicalAttemptOrderAcrossRandomIDs(t *testing.T) {
	baseline := coveredInteraction(t)
	for _, identities := range [][2]string{
		{"attempt:ffffffffffffffffffffffffffffffff", "attempt:00000000000000000000000000000001"},
		{"attempt:00000000000000000000000000000002", "attempt:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
	} {
		config := trajectoryConfig(baseline)
		first, second := config.ToolCalls[0].Clone(), config.ToolCalls[0].Clone()
		if err := first.AttemptID.UnmarshalText([]byte(identities[0])); err != nil {
			t.Fatal(err)
		}
		if err := second.AttemptID.UnmarshalText([]byte(identities[1])); err != nil {
			t.Fatal(err)
		}
		first.Outcome, first.Result, first.Failure = trajectory.ToolOutcomeUnknown, nil, "uncertain first attempt"
		config.ToolCalls = []trajectory.ToolCall{first, second}
		var events []agent.Event
		var started agent.Event
		var sequence uint64
		for _, event := range config.Events {
			if event.ProcessID() != first.ProcessID {
				events = append(events, event)
				continue
			}
			sequence++
			if fact, ok := event.EffectStarted(); ok && fact.AttemptID() == baseline.ToolCalls()[0].AttemptID {
				started = event
				events = append(events, changeAttemptEvent(t, event, sequence, first.AttemptID, ""))
				continue
			}
			if fact, ok := event.EffectFinished(); ok && fact.AttemptID() == baseline.ToolCalls()[0].AttemptID {
				events = append(events, changeAttemptEvent(t, event, sequence, first.AttemptID, agent.SettlementStatusUnknown))
				sequence++
				events = append(events, changeAttemptEvent(t, started, sequence, second.AttemptID, ""))
				sequence++
				events = append(events, changeAttemptEvent(t, event, sequence, second.AttemptID, agent.SettlementStatusSucceeded))
				sequence++
				events = append(events, changeEvent(t, event, map[string]any{
					"process_sequence": sequence, "name": agent.EventEffectResolved, "phase": agent.EventPhaseCommitted,
					"payload": map[string]any{"effect_target": agent.EffectTargetDispatcher, "settlement_status": agent.SettlementStatusSucceeded},
				}))
				continue
			}
			events = append(events, changeEvent(t, event, map[string]any{"process_sequence": sequence}))
		}
		config.Events = events
		recorded, err := trajectory.New(config)
		if err != nil {
			t.Fatal(err)
		}
		report, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: recorded, Expected: trajectory.Expectation{
			Status: agent.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{
				{Name: "weather", Outcome: trajectory.ToolOutcomeUnknown},
				{Name: "weather", Outcome: trajectory.ToolOutcomeSucceeded},
			}},
		}})
		if err != nil || report.Verdict() != eval.VerdictPass {
			t.Fatalf("random physical IDs reversed observed settlement order: report=%+v error=%v", report, err)
		}
	}
}

// Imported replay evidence uses actual attempt order and the Engine's current
// event schema. The fixture does not change ToolSet's ReplayPolicyNever.
func changeAttemptEvent(t *testing.T, event agent.Event, sequence uint64, attempt agent.EffectAttemptID, status agent.SettlementStatus) agent.Event {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := jsonv2.Unmarshal(event.Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	payload["attempt_id"] = encoded
	if status.Valid() {
		encoded, err := jsonv2.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		payload["settlement_status"] = encoded
	}
	return changeEvent(t, event, map[string]any{"process_sequence": sequence, "payload": payload})
}

func TestSemanticCallsCannotChangeTheirOwningStep(t *testing.T) {
	baseline := coveredInteraction(t)
	for _, model := range []bool{true, false} {
		config := trajectoryConfig(baseline)
		if model {
			config.ModelCalls[0].StepSequence++
		} else {
			config.ToolCalls[0].StepSequence++
		}
		if _, err := trajectory.New(config); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
			t.Fatalf("fabricated call attribution was accepted: %v", err)
		}
	}
}
