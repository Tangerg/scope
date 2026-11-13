package run

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidExecutionState = errors.New("run: invalid execution state")

// ExecutionState is an immutable envelope owned by one Execution Strategy. The
// Runtime persists and returns Payload without interpreting it. Its zero value
// is invalid.
type ExecutionState struct {
	kind    string
	payload Payload
}

// EncodeExecutionState strictly encodes a typed strategy state. Invalid UTF-8
// and duplicate JSON names are rejected.
func EncodeExecutionState[T any](kind string, value T) (ExecutionState, error) {
	payload, err := EncodePayload(value)
	if err != nil {
		return ExecutionState{}, fmt.Errorf("%w: encode: %w", ErrInvalidExecutionState, err)
	}
	return NewExecutionState(kind, payload)
}

// NewExecutionState freezes an opaque payload under its owning Strategy kind.
func NewExecutionState(kind string, payload Payload) (ExecutionState, error) {
	if !ValidQualifiedName(kind) {
		return ExecutionState{}, fmt.Errorf("%w: kind must be a lowercase qualified name", ErrInvalidExecutionState)
	}
	if !payload.Valid() {
		return ExecutionState{}, fmt.Errorf("%w: payload is required", ErrInvalidExecutionState)
	}
	return ExecutionState{kind: kind, payload: payload}, nil
}

func (e ExecutionState) Kind() string { return e.kind }

func (e ExecutionState) Payload() Payload { return e.payload }

// Decode checks the Strategy kind before decoding its payload into T. Unknown
// object members are rejected; the Strategy still validates its domain state.
func (e ExecutionState) Decode[T any](kind string) (T, error) {
	if !e.Valid() || e.kind != kind {
		var value T
		return value, fmt.Errorf("%w: expected Strategy kind %q, got %q", ErrInvalidExecutionState, kind, e.kind)
	}
	value, err := e.payload.Decode[T]()
	if err != nil {
		return value, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	return value, nil
}

func (e ExecutionState) Valid() bool { return e.kind != "" }

func (e ExecutionState) MarshalJSON() ([]byte, error) {
	if !e.Valid() {
		return nil, ErrInvalidExecutionState
	}
	return jsonv2.Marshal(executionStateWire{Kind: e.kind, Payload: e.payload})
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

type executionStateWire struct {
	Kind    string  `json:"kind"`
	Payload Payload `json:"payload"`
}
