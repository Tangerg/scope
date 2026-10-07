package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
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
	// status is present exactly on a Dispatcher settlement, whose status owns
	// whether its Effect succeeded; the payload owns only the result data.
	status SettlementStatus
}

// NewSignal builds one delivery envelope with a canonical payload for
// Definition tests and conformance cases. Constructing one does not admit or
// deliver it: at runtime the Engine mints every Signal, and
// Process.DeliverSignals accepts only a SignalRequest.
func NewSignal(id SignalID, waitID WaitID, payload json.RawMessage) (Signal, error) {
	if !id.Valid() {
		return Signal{}, fmt.Errorf("%w: %w", ErrInvalidSignal, ErrInvalidIdentity)
	}
	normalized, err := normalizeJSON(payload, MaxPayloadBytes)
	if err != nil {
		return Signal{}, fmt.Errorf("%w: payload: %w", ErrInvalidSignal, err)
	}
	return Signal{id: id, waitID: waitID, payload: normalized}, nil
}

// deliversSettlement reports whether status may accompany a delivery: only an
// Engine-minted, unaddressed delivery of a definite Dispatcher settlement
// carries one, because an unknown outcome stays with the Host.
func deliversSettlement(id SignalID, waitID WaitID, status SettlementStatus) bool {
	return id.engineOwned() && !waitID.Valid() && status.Valid() && status != SettlementStatusUnknown
}

// NewSettlementSignal builds the delivery of a definite Dispatcher settlement
// for Definition tests and conformance cases. At runtime only the Engine mints
// it, under the settlement identity of the Effect it settles.
func NewSettlementSignal(id SignalID, settlement Settlement) (Signal, error) {
	if !settlement.Valid() || !deliversSettlement(id, WaitID{}, settlement.status) {
		return Signal{}, fmt.Errorf("%w: settlement delivery requires an Engine identity and a definite settlement", ErrInvalidSignal)
	}
	signal, err := NewSignal(id, WaitID{}, settlement.payload)
	if err != nil {
		return Signal{}, err
	}
	signal.status = settlement.status
	return signal, nil
}

// ID returns the stable delivery and deduplication identity.
func (s Signal) ID() SignalID { return s.id }

// EngineOwned reports whether the Engine produced this Signal as execution
// evidence. Ordinary delivery cannot use this authority. Like all restored
// execution facts, a decoded Signal is trustworthy only from trusted storage.
func (s Signal) EngineOwned() bool { return s.Valid() && s.id.engineOwned() }

// Settles reports whether the Engine minted this delivery for effectID's
// settlement, without exposing the private delivery identity. A wait opening
// is identified by the WaitID it addresses instead. As with EngineOwned,
// decoded Signals must come from trusted storage.
func (s Signal) Settles(effectID EffectID) bool {
	return s.EngineOwned() && effectID.Valid() && s.id == effectID.settlementSignalID()
}

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
		Status:  s.status,
	}
	if s.waitID.Valid() {
		wire.WaitID = &s.waitID
	}
	return jsonv2.Marshal(wire)
}

func (s *Signal) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidSignal)
	}
	wire, err := jsonwire.Decode[signalWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidSignal, err)
	}
	var waitID WaitID
	if wire.WaitID != nil {
		waitID = *wire.WaitID
	}
	value, err := NewSignal(wire.ID, waitID, wire.Payload)
	if err != nil {
		return err
	}
	if wire.Status != SettlementStatusInvalid {
		if !deliversSettlement(wire.ID, waitID, wire.Status) {
			return fmt.Errorf("%w: only an Engine delivery of a definite settlement has a status", ErrInvalidSignal)
		}
		value.status = wire.Status
	}
	*s = value
	return nil
}

type signalWire struct {
	ID      SignalID         `json:"id"`
	WaitID  *WaitID          `json:"wait_id,omitzero"`
	Payload json.RawMessage  `json:"payload"`
	Status  SettlementStatus `json:"status,omitzero"`
}

func (Signal) JSONSchemaAlias() any { return signalWire{} }
