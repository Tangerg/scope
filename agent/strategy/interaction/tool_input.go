package interaction

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/tool"
)

// NewToolInputResponseSignal validates JSON and addresses the exact wait.
// Execution checks the authoritative response schema; PendingToolInput.ResponseSignal
// also validates that schema locally.
func NewToolInputResponseSignal(
	id agent.SignalID,
	waitID agent.WaitID,
	response json.RawMessage,
) (agent.SignalRequest, error) {
	if !waitID.Valid() {
		return agent.SignalRequest{}, fmt.Errorf("%w: WaitID is required", tool.ErrInvalidInputRequest)
	}
	input, err := parseToolInputJSON(response)
	if err != nil {
		return agent.SignalRequest{}, fmt.Errorf("%w: response: %w", tool.ErrInvalidInputRequest, err)
	}
	payload, err := jsonv2.Marshal(signalEnvelope{
		InputResponse: input.JSON(),
	}, jsonv2.Deterministic(true))
	if err != nil {
		return agent.SignalRequest{}, err
	}
	return agent.NewSignalRequest(id, waitID, payload)
}

func parseToolInputJSON(data json.RawMessage) (agent.Payload, error) {
	if len(data) == 0 || len(data) > tool.MaxInputBytes {
		return agent.Payload{}, fmt.Errorf("JSON value must contain at most %d bytes", tool.MaxInputBytes)
	}
	input, err := agent.ParsePayload(data)
	if err != nil {
		return agent.Payload{}, err
	}
	if len(input.JSON()) > tool.MaxInputBytes {
		return agent.Payload{}, fmt.Errorf("normalized JSON value exceeds %d bytes", tool.MaxInputBytes)
	}
	return input, nil
}
