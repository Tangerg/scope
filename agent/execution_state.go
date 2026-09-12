package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidExecutionState = errors.New("agent: invalid execution state")

// ExecutionState is an immutable envelope owned by one Execution Strategy. The
// Engine persists and returns Payload without interpreting it. Its zero value
// is invalid.
type ExecutionState struct {
	kind    string
	payload json.RawMessage
}

// NewExecutionState pairs a strategy kind with an opaque payload. The kind
// exists so a snapshot can be rejected when restored into the wrong strategy;
// the payload stays opaque so adding a strategy never widens the kernel.
func NewExecutionState(kind string, payload json.RawMessage) (ExecutionState, error) {
	if !validQualifiedName(kind) {
		return ExecutionState{}, fmt.Errorf("%w: kind must be a lowercase qualified name", ErrInvalidExecutionState)
	}
	normalized, err := wireJSON.normalize(payload, maxWireBytes)
	if err != nil {
		return ExecutionState{}, fmt.Errorf("%w: payload: %w", ErrInvalidExecutionState, err)
	}
	return ExecutionState{kind: kind, payload: normalized}, nil
}

// Kind returns the Strategy that exclusively interprets Payload.
func (e ExecutionState) Kind() string { return e.kind }

// Payload returns an independently owned copy of the opaque Strategy state.
func (e ExecutionState) Payload() json.RawMessage { return bytes.Clone(e.payload) }

func (e ExecutionState) Valid() bool {
	return validQualifiedName(e.kind) && len(e.payload) > 0
}

func (e ExecutionState) clone() ExecutionState {
	return ExecutionState{kind: e.kind, payload: bytes.Clone(e.payload)}
}

func (e ExecutionState) MarshalJSON() ([]byte, error) {
	if !e.Valid() {
		return nil, ErrInvalidExecutionState
	}
	return json.Marshal(executionStateWire{
		Kind:    e.kind,
		Payload: e.payload,
	})
}

func (e *ExecutionState) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidExecutionState)
	}
	wire, err := wireJSON.decode[executionStateWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidExecutionState, err)
	}
	value, err := NewExecutionState(wire.Kind, wire.Payload)
	if err != nil {
		return err
	}
	*e = value
	return nil
}

func (e ExecutionState) digest() (Digest, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return Digest{}, err
	}
	return digestBytes(data), nil
}

type executionStateWire struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}
