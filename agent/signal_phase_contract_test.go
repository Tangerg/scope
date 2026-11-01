package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestInteractionRejectsSignalsThatCannotAnswerItsCurrentPhase(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	input, err := run.EncodePayload(Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := fresh.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, stepErr := fresh.Step(t.Context(), nil); stepErr != nil {
		t.Fatal(stepErr)
	}
	awaiting, err := fresh.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	message := chat.NewAssistantMessage(chat.NewTextPart("done"))
	modelResult := signalEnvelope{ModelResult: &modelCallResult{Response: &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}}}
	toolResult := signalEnvelope{ToolResult: &toolDispatchResult{Completion: &toolCallResult{Disposition: ResultSucceeded, Output: toolcontract.NewTextOutput("done")}}}
	steer := signalEnvelope{Steer: &steerInput{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("continue"))}}}
	modelSignal := phaseContractSignal(t, "signal:engine:model", "", "succeeded", modelResult)
	addressedSteer := phaseContractSignal(t, "signal:steer", "wait:tool", "", steer)
	malformed := phaseContractSignal(t, "signal:malformed", "", "", json.RawMessage(`{}`))
	for _, test := range []struct {
		name    string
		state   run.ExecutionState
		signals []run.Signal
	}{
		{"ready malformed", ready, []run.Signal{malformed}},
		{"ready model settlement", ready, []run.Signal{modelSignal}},
		{"ready addressed steer", ready, []run.Signal{addressedSteer}},
		{"missing settlement", awaiting, nil},
		{"duplicate settlement", awaiting, []run.Signal{modelSignal, modelSignal}},
		{"wrong settlement operation", awaiting, []run.Signal{phaseContractSignal(t, "signal:engine:tool", "", "succeeded", toolResult)}},
		{"failed settlement without diagnostic", awaiting, []run.Signal{phaseContractSignal(t, "signal:engine:model", "", "failed", json.RawMessage(`{}`))}},
		{"failed settlement with empty diagnostic", awaiting, []run.Signal{phaseContractSignal(t, "signal:engine:model", "", "failed", "")}},
		{"malformed while awaiting", awaiting, []run.Signal{malformed}},
		{"addressed steer while awaiting", awaiting, []run.Signal{addressedSteer, modelSignal}},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution, err := definition.Restore(t.Context(), test.state)
			if err != nil {
				t.Fatal(err)
			}
			if transition, stepErr := execution.Step(t.Context(), test.signals); stepErr == nil || transition.Valid() {
				t.Fatalf("unowned signal admitted: %+v, %v", transition, stepErr)
			}
			after, err := execution.Snapshot()
			if err != nil || !bytes.Equal(test.state.Payload(), after.Payload()) {
				t.Fatalf("rejected signal changed recovery state: %v", err)
			}
		})
	}
}

func phaseContractSignal(t testing.TB, id, waitID, status string, payload any) run.Signal {
	t.Helper()
	encoded, err := jsonv2.Marshal(struct {
		ID      string `json:"id"`
		WaitID  string `json:"wait_id,omitempty"`
		Status  string `json:"status,omitempty"`
		Payload any    `json:"payload"`
	}{ID: id, WaitID: waitID, Status: status, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var signal run.Signal
	if err := jsonv2.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}
