package run

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidSignal = errors.New("run: invalid signal")

// Signal is the immutable input envelope delivered by the Runtime to an
// Execution. Dispatcher and ordinary wait payloads belong exclusively to the
// Strategy; Framework composition payloads are decoded only through their
// typed frames. SignalID identifies delivery, while an optional WaitID
// identifies the Runtime-created wait target.
type Signal struct {
	id      SignalID
	waitID  WaitID
	payload json.RawMessage
	// status is present exactly on a Dispatcher settlement, whose status owns
	// whether its Effect succeeded; the payload owns only the result data.
	status SettlementStatus
	frame  SignalFrame
}

// NewSignal builds one delivery envelope with a canonical payload for
// Definition tests and conformance cases. Constructing one does not admit or
// deliver it: at runtime the Runtime mints every Signal, and
// Handle.DeliverSignals accepts only a SignalRequest.
func NewSignal(id SignalID, waitID WaitID, payload json.RawMessage) (Signal, error) {
	return newSignal(id, waitID, payload, SettlementStatusInvalid)
}

func newSignal(id SignalID, waitID WaitID, payload json.RawMessage, status SettlementStatus) (Signal, error) {
	if !id.Valid() {
		return Signal{}, fmt.Errorf("%w: %w", ErrInvalidSignal, ErrInvalidIdentity)
	}
	normalized, err := normalizeJSON(payload, MaxPayloadBytes)
	if err != nil {
		return Signal{}, fmt.Errorf("%w: payload: %w", ErrInvalidSignal, err)
	}
	return materializeSignal(id, waitID, normalized, status)
}

// deliversSettlement reports whether status may accompany a delivery: only an
// Runtime-minted, unaddressed delivery of a definite Dispatcher settlement
// carries one, because an unknown outcome stays with the Host.
func deliversSettlement(id SignalID, waitID WaitID, status SettlementStatus) bool {
	return id.engineOwned() && !waitID.Valid() && status.Valid() && status != SettlementStatusUnknown
}

// NewSettlementSignal builds the delivery of a definite Dispatcher settlement
// for Definition tests and conformance cases. At runtime only the Runtime mints
// it, under the settlement identity of the Effect it settles.
func NewSettlementSignal(id SignalID, settlement Settlement) (Signal, error) {
	if !settlement.Valid() || !deliversSettlement(id, WaitID{}, settlement.status) {
		return Signal{}, fmt.Errorf("%w: settlement delivery requires an Engine identity and a definite settlement", ErrInvalidSignal)
	}
	return materializeSignal(id, WaitID{}, settlement.payload, settlement.status)
}

// Frame returns the selected, immutable delivery variant. Use SignalFrame itself
// for a type switch over a mixed inbox. Dispatcher output and external input
// remain opaque even when their JSON resembles a framework operation.
func (s Signal) Frame[T SignalFrame]() (T, error) {
	var zero T
	if !s.Valid() {
		return zero, ErrInvalidSignal
	}
	value, ok := s.frame.(T)
	if !ok {
		return zero, fmt.Errorf("%w: got %T, want %T", ErrInvalidSignal, s.frame, zero)
	}
	return value, nil
}

func materializeSignal(id SignalID, wait WaitID, payload json.RawMessage, status SettlementStatus) (Signal, error) {
	signal := Signal{id: id, waitID: wait, payload: payload, status: status}
	frame, err := signal.decodeFrame()
	if err != nil {
		return Signal{}, fmt.Errorf("%w: %w", ErrInvalidSignal, err)
	}
	signal.frame = frame
	return signal, nil
}

func (s Signal) decodeFrame() (SignalFrame, error) {
	if !s.id.engineOwned() {
		return ExternalInput{payload: s.payload}, nil
	}
	if s.status != SettlementStatusInvalid {
		return Settlement{status: s.status, payload: s.payload}, nil
	}
	if s.waitID.Valid() {
		switch {
		case bytes.Equal(s.payload, waitOpenedPayload()):
			return WaitOpened{waitID: s.waitID}, nil
		case bytes.Equal(s.payload, childWaitOpenedPayload()):
			return ChildWaitOpened{waitID: s.waitID}, nil
		default:
			return decodeChildWaitSatisfied(s.waitID, s.payload)
		}
	}
	var header frameworkOperationHeader
	if err := jsonv2.Unmarshal(s.payload, &header); err != nil {
		return nil, fmt.Errorf("%w: framework delivery: %w", ErrInvalidSignal, err)
	}
	switch header.Operation {
	case frameworkOperationStartChild:
		return decodeChildStartResult(s.payload)
	case childControlSettledOperation:
		return decodeChildControlResult(s.payload)
	default:
		return nil, fmt.Errorf("%w: unknown framework delivery %q", ErrInvalidSignal, header.Operation)
	}
}

// ID returns the stable delivery and deduplication identity.
func (s Signal) ID() SignalID { return s.id }

// Settles reports whether the Runtime minted this delivery for effectID's
// settlement, without exposing the private delivery identity. A wait opening
// is identified by the WaitID it addresses instead. Decoded signals must come
// from trusted storage.
func (s Signal) Settles(effectID EffectID) bool {
	return s.Valid() && effectID.Valid() && s.id == effectID.settlementSignalID()
}

// WaitID returns the addressed wait and true, or a zero WaitID and false for a
// Signal queued at the next Strategy-safe boundary.
func (s Signal) WaitID() (WaitID, bool) { return s.waitID, s.waitID.Valid() }

// Payload returns an independently owned copy. Strategy-owned payloads are
// interpreted only by their Strategy; Framework-owned payloads should be read
// through Frame.
func (s Signal) Payload() json.RawMessage { return bytes.Clone(s.payload) }

func (s Signal) Valid() bool { return s.frame != nil }

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
	if wire.Status != SettlementStatusInvalid && !deliversSettlement(wire.ID, waitID, wire.Status) {
		return fmt.Errorf("%w: only an engine delivery of a definite settlement has a status", ErrInvalidSignal)
	}
	value, err := newSignal(wire.ID, waitID, wire.Payload, wire.Status)
	if err != nil {
		return err
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

func (Signal) JSONSchemaModel() any { return signalWire{} }
