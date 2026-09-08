package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidSignal = errors.New("agent: invalid signal")

// Signal is the immutable input envelope delivered by the Engine to an
// Execution. Dispatcher and ordinary wait payloads belong exclusively to the
// Strategy; Framework composition payloads are decoded only through their
// public typed helpers. SignalID identifies delivery, while an optional WaitID
// identifies the Engine-created wait target.
type Signal struct {
	id      SignalID
	waitID  WaitID
	payload json.RawMessage
}

func newSignal(id SignalID, waitID WaitID, payload json.RawMessage) (Signal, error) {
	if !id.Valid() {
		return Signal{}, fmt.Errorf("%w: %w", ErrInvalidSignal, ErrInvalidIdentity)
	}
	normalized, err := wireJSON.normalize(payload, maxWireBytes)
	if err != nil {
		return Signal{}, fmt.Errorf("%w: payload: %w", ErrInvalidSignal, err)
	}
	return Signal{id: id, waitID: waitID, payload: normalized}, nil
}

// ID returns the stable delivery and deduplication identity.
func (s Signal) ID() SignalID { return s.id }

// WaitID returns the addressed wait and true, or a zero WaitID and false for a
// Signal queued at the next Strategy-safe boundary.
func (s Signal) WaitID() (WaitID, bool) { return s.waitID, s.waitID.Valid() }

// Payload returns an independently owned copy. Strategy-owned payloads are
// interpreted only by their Strategy; Framework-owned payloads should be read
// through the corresponding typed parser rather than decoded ad hoc.
func (s Signal) Payload() json.RawMessage { return bytes.Clone(s.payload) }

func (s Signal) Valid() bool { return s.id.Valid() && len(s.payload) > 0 }

func (s Signal) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalidSignal
	}
	wire := signalWire{
		ID:      s.id,
		Payload: s.payload,
	}
	if s.waitID.Valid() {
		wire.WaitID = &s.waitID
	}
	return json.Marshal(wire)
}

func (s *Signal) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidSignal)
	}
	wire, err := wireJSON.decode[signalWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidSignal, err)
	}
	var waitID WaitID
	if wire.WaitID != nil {
		waitID = *wire.WaitID
	}
	value, err := newSignal(wire.ID, waitID, wire.Payload)
	if err != nil {
		return err
	}
	*s = value
	return nil
}

type signalWire struct {
	ID      SignalID        `json:"id"`
	WaitID  *WaitID         `json:"wait_id,omitempty"`
	Payload json.RawMessage `json:"payload"`
}
