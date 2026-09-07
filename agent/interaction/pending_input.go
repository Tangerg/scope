package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

var ErrInvalidPendingToolInput = errors.New("interaction: invalid pending tool input")

// PendingToolInput is the consumer-facing view of one current Tool input wait.
// It deliberately excludes Tool continuation state and all application UI,
// persistence, approval, or actor concepts.
type PendingToolInput struct {
	waitID         agent.WaitID
	prompt         json.RawMessage
	responseSchema agent.Schema
}

// WaitID returns the Engine-minted identity required to address the response.
func (p PendingToolInput) WaitID() agent.WaitID { return p.waitID }

// Prompt returns an independently owned Tool-defined JSON prompt.
func (p PendingToolInput) Prompt() json.RawMessage { return bytes.Clone(p.prompt) }

// ResponseSchema returns the authoritative JSON Schema for a response.
func (p PendingToolInput) ResponseSchema() json.RawMessage {
	return p.responseSchema.JSON()
}

func (p PendingToolInput) Valid() bool {
	return p.waitID.Valid() && len(p.prompt) > 0 && p.responseSchema.Valid()
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

// PendingToolInputFromProcess captures process and interprets its committed
// state only when it is an Interaction currently waiting for Tool input.
func PendingToolInputFromProcess(
	ctx context.Context,
	process *agent.Process,
) (PendingToolInput, bool, error) {
	if process == nil {
		return PendingToolInput{}, false, ErrInvalidPendingToolInput
	}
	snapshot, err := process.Snapshot(ctx)
	if err != nil {
		return PendingToolInput{}, false, err
	}
	return PendingToolInputFromSnapshot(snapshot)
}

// PendingToolInputFromSnapshot interprets only Interaction-owned state. A
// valid non-Waiting or non-Interaction snapshot returns found=false.
func PendingToolInputFromSnapshot(snapshot agent.ProcessSnapshot) (PendingToolInput, bool, error) {
	if !snapshot.Valid() {
		return PendingToolInput{}, false, ErrInvalidPendingToolInput
	}
	if snapshot.Status() != agent.StatusWaiting {
		return PendingToolInput{}, false, nil
	}
	stateEnvelope := snapshot.CommittedExecutionState()
	if stateEnvelope.Kind() != executionStateKind {
		return PendingToolInput{}, false, nil
	}
	var state executionState
	if err := jsonv2.Unmarshal(stateEnvelope.Payload(), &state, jsonv2.RejectUnknownMembers(true)); err != nil {
		return PendingToolInput{}, false, fmt.Errorf("%w: decode state: %w", ErrInvalidPendingToolInput, err)
	}
	outerWaitID, ok := snapshot.WaitID()
	switch state.Phase {
	case phaseWaitingDelegates:
		if _, err := state.activeDelegateCalls(); err != nil {
			return PendingToolInput{}, false, fmt.Errorf("%w: %w", ErrInvalidPendingToolInput, err)
		}
		if state.WaitID == nil || !ok || outerWaitID != *state.WaitID {
			return PendingToolInput{}, false, ErrInvalidPendingToolInput
		}
		return PendingToolInput{}, false, nil
	case phaseWaitingInput:
	default:
		return PendingToolInput{}, false, ErrInvalidPendingToolInput
	}
	if err := state.validatePendingToolInput(); err != nil {
		return PendingToolInput{}, false, fmt.Errorf("%w: %w", ErrInvalidPendingToolInput, err)
	}
	if state.WaitID == nil || !ok || outerWaitID != *state.WaitID || state.ToolCheckpoint == nil {
		return PendingToolInput{}, false, ErrInvalidPendingToolInput
	}
	request, err := state.ToolCheckpoint.InputRequest.inputRequest()
	if err != nil {
		return PendingToolInput{}, false, fmt.Errorf("%w: %w", ErrInvalidPendingToolInput, err)
	}
	return PendingToolInput{
		waitID: outerWaitID, prompt: request.Prompt(), responseSchema: request.responseSchema,
	}, true, nil
}
