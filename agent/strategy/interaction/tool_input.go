package interaction

import (
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/tool"
)

// NewToolInputResponseSignal validates JSON and addresses the exact wait.
// Its payload is the response JSON itself, so the wait enforces the original
// response Schema without rebasing references or changing its resource scope.
// PendingToolInput.ResponseSignal also validates that Schema locally.
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
	return agent.NewSignalRequest(id, waitID, input.JSON())
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
