package interaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

var ErrInvalidPendingToolInput = errors.New("interaction: invalid pending tool input")

// PendingToolInput is the consumer-facing view of one current Tool input wait.
// It deliberately excludes Tool continuation state and all application UI,
// persistence, approval, or actor concepts.
type PendingToolInput struct {
	processID      agent.ProcessID
	waitID         agent.WaitID
	prompt         json.RawMessage
	responseSchema agent.Schema
}

// ProcessID returns the Tool child that must receive the response.
func (p PendingToolInput) ProcessID() agent.ProcessID { return p.processID }

// WaitID returns the Engine-minted identity required to address the response.
func (p PendingToolInput) WaitID() agent.WaitID { return p.waitID }

// Prompt returns an independently owned Tool-defined JSON prompt.
func (p PendingToolInput) Prompt() json.RawMessage { return bytes.Clone(p.prompt) }

// ResponseSchema returns the authoritative JSON Schema for a response.
func (p PendingToolInput) ResponseSchema() json.RawMessage {
	return p.responseSchema.JSON()
}

func (p PendingToolInput) Valid() bool {
	return p.processID.Valid() && p.waitID.Valid() && len(p.prompt) > 0 && p.responseSchema.Valid()
}

// ResponseSignal validates response locally against ResponseSchema and returns
// one WaitID-addressed SignalRequest with caller-supplied deduplication ID.
func (p PendingToolInput) ResponseSignal(
	id agent.SignalID,
	response json.RawMessage,
) (agent.SignalRequest, error) {
	if !p.Valid() {
		return agent.SignalRequest{}, ErrInvalidPendingToolInput
	}
	response, err := validateToolInputResponse(p.responseSchema, response)
	if err != nil {
		return agent.SignalRequest{}, err
	}
	return NewToolInputResponseSignal(id, p.waitID, response)
}

// PendingToolInputs reads every current Tool input wait in a captured tree.
// The returned order follows the snapshot's Process order. The caller selects
// a wait explicitly and sends its ResponseSignal to that wait's ProcessID.
func PendingToolInputs(snapshot agent.TreeSnapshot) ([]PendingToolInput, error) {
	if !snapshot.Valid() {
		return nil, ErrInvalidPendingToolInput
	}
	var pending []PendingToolInput
	for _, process := range snapshot.ProcessSnapshots() {
		if process.Status() != agent.StatusWaiting || process.CommittedExecutionState().Kind() != toolExecutionStateKind {
			continue
		}
		state, err := decodeToolState(process.CommittedExecutionState())
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidPendingToolInput, err)
		}
		waitID, addressed := process.WaitID()
		if state.Phase != toolWaitingInput || !addressed || state.WaitID == nil || waitID != *state.WaitID {
			return nil, ErrInvalidPendingToolInput
		}
		request, err := state.Checkpoint.InputRequest.inputRequest()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidPendingToolInput, err)
		}
		pending = append(pending, PendingToolInput{processID: process.ProcessID(), waitID: waitID, prompt: request.Prompt(), responseSchema: request.responseSchema})
	}
	return pending, nil
}
