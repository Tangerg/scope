package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidSignalRequest = errors.New("agent: invalid signal request")

// SignalRequest is an immutable request to deliver Strategy-owned input to a
// Process. ID supplies caller-stable deduplication. WaitID is zero for ordinary
// next-boundary input and Engine-minted for an external wait answer.
type SignalRequest struct {
	id      SignalID
	waitID  WaitID
	payload json.RawMessage
}

// NewSignalRequest requires a caller-chosen SignalID outside the reserved
// signal:engine: namespace, so a Host retry after an ambiguous failure stays
// one logical delivery rather than a second answer.
func NewSignalRequest(id SignalID, waitID WaitID, payload json.RawMessage) (SignalRequest, error) {
	if !id.Valid() || id.engineOwned() {
		return SignalRequest{}, fmt.Errorf("%w: signal ID: %w", ErrInvalidSignalRequest, ErrInvalidIdentity)
	}
	normalized, err := normalizeJSON(payload, MaxPayloadBytes)
	if err != nil {
		return SignalRequest{}, fmt.Errorf("%w: payload: %w", ErrInvalidSignalRequest, err)
	}
	return SignalRequest{id: id, waitID: waitID, payload: normalized}, nil
}

// ID returns the stable delivery and deduplication identity.
func (s SignalRequest) ID() SignalID { return s.id }

func (s SignalRequest) WaitID() (WaitID, bool) { return s.waitID, s.waitID.Valid() }

// Payload returns an independently owned Strategy-defined value.
func (s SignalRequest) Payload() json.RawMessage { return bytes.Clone(s.payload) }

func (s SignalRequest) Valid() bool { return s.id.Valid() && !s.id.engineOwned() && len(s.payload) > 0 }

func (s SignalRequest) signal() (Signal, error) {
	if !s.Valid() {
		return Signal{}, ErrInvalidSignalRequest
	}
	return NewSignal(s.id, s.waitID, s.payload)
}

func (s SignalRequest) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalidSignalRequest
	}
	wire := signalRequestWire{ID: s.id, Payload: s.payload}
	if s.waitID.Valid() {
		wire.WaitID = &s.waitID
	}
	return jsonv2.Marshal(wire)
}

func (s *SignalRequest) UnmarshalJSON(data []byte) error {
	if s == nil {
		return ErrInvalidSignalRequest
	}
	wire, err := jsonwire.Decode[signalRequestWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidSignalRequest, err)
	}
	request, err := NewSignalRequest(wire.ID, lo.FromPtr(wire.WaitID), wire.Payload)
	if err != nil {
		return err
	}
	*s = request
	return nil
}

// signalRequestWire has no settlement status: only the Engine mints a
// settlement delivery.
type signalRequestWire struct {
	ID      SignalID        `json:"id"`
	WaitID  *WaitID         `json:"wait_id,omitzero"`
	Payload json.RawMessage `json:"payload"`
}

func (SignalRequest) JSONSchemaAlias() any { return signalRequestWire{} }
