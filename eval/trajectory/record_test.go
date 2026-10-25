package trajectory_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestCallOutcomesProjectTheirSettlementFacts(t *testing.T) {
	recorder := new(trajectory.Recorder)
	process := runRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{})
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	model := recorded.ModelCalls()[0]
	response := model.Response
	model.Response = nil
	model.Outcome = interaction.ModelOutcomeUnobserved
	if model.Validate() != nil {
		t.Fatal("an absent model settlement became a result")
	}
	model.Outcome = interaction.ModelOutcomeUnknown
	if model.Validate() != nil {
		t.Fatal("explicit model uncertainty was lost")
	}
	model.Response = response
	if model.Validate() == nil {
		t.Fatal("accepted a model response alongside uncertainty")
	}

	call := recorded.ToolCalls()[0]
	call.Unknown = true
	if call.Outcome() != trajectory.ToolOutcomeInvalid || call.Validate() == nil {
		t.Fatal("a tool result won over competing uncertainty")
	}
	call.Unknown = false
	call.Result.IsError = true
	if call.Outcome() != trajectory.ToolOutcomeError || call.Validate() != nil {
		t.Fatal("tool result did not own its error classification")
	}
	call.Result = nil
	if call.Outcome() != trajectory.ToolOutcomeUnobserved || call.Validate() != nil {
		t.Fatal("an absent tool settlement became a result")
	}
	call.Failure = "permission refused"
	if call.Outcome() != trajectory.ToolOutcomeFailed || call.Validate() != nil {
		t.Fatal("tool failure did not own its outcome")
	}
	call.Unknown = true
	if call.Outcome() != trajectory.ToolOutcomeUnknown || call.Validate() != nil {
		t.Fatal("an uncertainty diagnostic became a definite failure")
	}
	call.InputRequired = true
	if call.Outcome() != trajectory.ToolOutcomeInvalid || call.Validate() == nil {
		t.Fatal("accepted competing input and uncertainty settlements")
	}
	call.Unknown, call.Failure = false, ""
	if call.Outcome() != trajectory.ToolOutcomeInputRequired || call.Validate() != nil {
		t.Fatal("tool input requirement was lost")
	}
}

func TestTrajectoryWireUsesOnlyCurrentModelOutcome(t *testing.T) {
	recorder := new(trajectory.Recorder)
	process := runRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{})
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"missing model outcome", "old model unknown", "invalid model outcome", "competing model evidence", "redundant tool outcome"} {
		t.Run(mutation, func(t *testing.T) {
			collection := "model_calls"
			if mutation == "redundant tool outcome" {
				collection = "tool_calls"
			}
			var wire map[string]json.RawMessage
			if decodeErr := jsonv2.Unmarshal(encoded, &wire); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			var calls []map[string]json.RawMessage
			if decodeErr := jsonv2.Unmarshal(wire[collection], &calls); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if collection == "model_calls" && string(calls[0]["outcome"]) != `"succeeded"` {
				t.Fatal("model wire omitted its authoritative outcome")
			}
			switch mutation {
			case "missing model outcome":
				delete(calls[0], "outcome")
			case "old model unknown":
				calls[0]["unknown"] = json.RawMessage(`false`)
			case "invalid model outcome":
				calls[0]["outcome"] = json.RawMessage(`"invalid"`)
			case "competing model evidence":
				calls[0]["outcome"] = json.RawMessage(`"not_executed"`)
				calls[0]["failure"] = json.RawMessage(`"not dispatched"`)
			case "redundant tool outcome":
				calls[0]["outcome"] = json.RawMessage(`"succeeded"`)
			}
			wire[collection], err = jsonv2.Marshal(calls)
			if err != nil {
				t.Fatal(err)
			}
			invalid, err := jsonv2.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var decoded trajectory.Trajectory
			if decodeErr := jsonv2.Unmarshal(invalid, &decoded); decodeErr == nil {
				t.Fatalf("accepted %s", mutation)
			}
		})
	}
}
