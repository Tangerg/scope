package interaction

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestInteractionRejectsSignalsThatCannotAnswerItsCurrentPhase(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	input, err := agent.EncodePayload(Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
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
	toolResult := signalEnvelope{ToolResult: &toolDispatchResult{Completion: &toolCallResult{Disposition: ResultSucceeded, Output: chat.NewTextToolOutput("done")}}}
	steer := signalEnvelope{Steer: &steerInput{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("continue"))}}}
	modelSignal := phaseContractSignal(t, "signal:engine:model", "", "succeeded", modelResult)
	addressedSteer := phaseContractSignal(t, "signal:steer", "wait:tool", "", steer)
	malformed := phaseContractSignal(t, "signal:malformed", "", "", json.RawMessage(`{}`))
	for _, test := range []struct {
		name    string
		state   agent.ExecutionState
		signals []agent.Signal
	}{
		{"ready malformed", ready, []agent.Signal{malformed}},
		{"ready model settlement", ready, []agent.Signal{modelSignal}},
		{"ready addressed steer", ready, []agent.Signal{addressedSteer}},
		{"missing settlement", awaiting, nil},
		{"duplicate settlement", awaiting, []agent.Signal{modelSignal, modelSignal}},
		{"wrong settlement operation", awaiting, []agent.Signal{phaseContractSignal(t, "signal:engine:tool", "", "succeeded", toolResult)}},
		{"failed settlement without diagnostic", awaiting, []agent.Signal{phaseContractSignal(t, "signal:engine:model", "", "failed", json.RawMessage(`{}`))}},
		{"failed settlement with empty diagnostic", awaiting, []agent.Signal{phaseContractSignal(t, "signal:engine:model", "", "failed", "")}},
		{"malformed while awaiting", awaiting, []agent.Signal{malformed}},
		{"addressed steer while awaiting", awaiting, []agent.Signal{addressedSteer, modelSignal}},
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

func phaseContractSignal(t testing.TB, id, waitID, status string, payload any) agent.Signal {
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
	var signal agent.Signal
	if err := jsonv2.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}
