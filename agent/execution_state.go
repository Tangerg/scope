package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
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
	if !ValidQualifiedName(kind) {
		return ExecutionState{}, fmt.Errorf("%w: kind must be a lowercase qualified name", ErrInvalidExecutionState)
	}
	normalized, err := normalizeJSON(payload, MaxPayloadBytes)
	if err != nil {
		return ExecutionState{}, fmt.Errorf("%w: payload: %w", ErrInvalidExecutionState, err)
	}
	return ExecutionState{kind: kind, payload: normalized}, nil
}

// EncodeExecutionState strictly encodes a typed strategy state and seals it with
// NewExecutionState. Invalid UTF-8 and duplicate JSON names are rejected.
func EncodeExecutionState[T any](kind string, value T) (ExecutionState, error) {
	payload, err := jsonv2.Marshal(value, jsonv2.Deterministic(true))
	if err != nil {
		return ExecutionState{}, fmt.Errorf("%w: encode: %w", ErrInvalidExecutionState, err)
	}
	return NewExecutionState(kind, payload)
}

// Kind returns the Strategy that exclusively interprets Payload.
func (e ExecutionState) Kind() string { return e.kind }

// Payload returns an independently owned copy of the opaque Strategy state.
func (e ExecutionState) Payload() json.RawMessage { return bytes.Clone(e.payload) }

// Decode checks the Strategy kind before decoding its payload into T. Unknown
// object members are rejected; the Strategy still validates its domain state.
func (e ExecutionState) Decode[T any](kind string) (T, error) {
	if !e.Valid() || e.kind != kind {
		var value T
		return value, fmt.Errorf("%w: expected Strategy kind %q, got %q", ErrInvalidExecutionState, kind, e.kind)
	}
	value, err := jsonwire.Decode[T](e.payload)
	if err != nil {
		return value, fmt.Errorf("%w: decode: %w", ErrInvalidExecutionState, err)
	}
	return value, nil
}

func (e ExecutionState) Valid() bool {
	return ValidQualifiedName(e.kind) && len(e.payload) > 0
}

func (e ExecutionState) clone() ExecutionState {
	return ExecutionState{kind: e.kind, payload: bytes.Clone(e.payload)}
}

func (e ExecutionState) MarshalJSON() ([]byte, error) {
	if !e.Valid() {
		return nil, ErrInvalidExecutionState
	}
	return jsonv2.Marshal(executionStateWire{
		Kind:    e.kind,
		Payload: e.payload,
	})
}

func (e *ExecutionState) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidExecutionState)
	}
	wire, err := jsonwire.Decode[executionStateWire](data)
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
	data, err := jsonv2.Marshal(e)
	if err != nil {
		return Digest{}, err
	}
	return digestBytes(data), nil
}

type executionStateWire struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}
