package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// NewSignalRequest requires a caller-chosen signal identity so that
// resubmitting the same delivery is exactly one logical consumption. Without
// it, a host retry after an ambiguous network failure would be
// indistinguishable from a second answer.
func NewSignalRequest(id SignalID, waitID WaitID, payload json.RawMessage) (SignalRequest, error) {
	if !id.Valid() {
		return SignalRequest{}, fmt.Errorf("%w: signal ID: %w", ErrInvalidSignalRequest, ErrInvalidIdentity)
	}
	normalized, err := wireJSON.normalize(payload, maxWireBytes)
	if err != nil {
		return SignalRequest{}, fmt.Errorf("%w: payload: %w", ErrInvalidSignalRequest, err)
	}
	return SignalRequest{id: id, waitID: waitID, payload: normalized}, nil
}

// ID returns the stable delivery and deduplication identity.
func (s SignalRequest) ID() SignalID { return s.id }

// WaitID returns the addressed wait and true, or a zero WaitID and false.
func (s SignalRequest) WaitID() (WaitID, bool) { return s.waitID, s.waitID.Valid() }

// Payload returns an independently owned Strategy-defined value.
func (s SignalRequest) Payload() json.RawMessage { return bytes.Clone(s.payload) }

func (s SignalRequest) Valid() bool { return s.id.Valid() && len(s.payload) > 0 }

func (s SignalRequest) signal() (Signal, error) {
	if !s.Valid() {
		return Signal{}, ErrInvalidSignalRequest
	}
	return newSignal(s.id, s.waitID, s.payload)
}

func (s SignalRequest) MarshalJSON() ([]byte, error) {
	signal, err := s.signal()
	if err != nil {
		return nil, err
	}
	return signal.MarshalJSON()
}

func (s *SignalRequest) UnmarshalJSON(data []byte) error {
	if s == nil {
		return ErrInvalidSignalRequest
	}
	var signal Signal
	if err := signal.UnmarshalJSON(data); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSignalRequest, err)
	}
	*s = SignalRequest(signal)
	return nil
}

func (SignalRequest) JSONSchemaAlias() any { return signalWire{} }
