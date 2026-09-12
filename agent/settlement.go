package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidSettlement = errors.New("agent: invalid effect settlement")

// SettlementStatus records whether an Effect definitely succeeded, definitely
// failed, or has an unknown external result. Unknown never implies safe retry.
type SettlementStatus string

const (
	// SettlementStatusInvalid is the invalid zero value.
	SettlementStatusInvalid SettlementStatus = ""
	// SettlementStatusSucceeded records a definite successful outcome.
	SettlementStatusSucceeded SettlementStatus = "succeeded"
	// SettlementStatusFailed records a definite failed outcome.
	SettlementStatusFailed SettlementStatus = "failed"
	// SettlementStatusUnknown records an externally indeterminate outcome.
	SettlementStatusUnknown SettlementStatus = "unknown"
)

func (s SettlementStatus) Valid() bool {
	switch s {
	case SettlementStatusSucceeded, SettlementStatusFailed, SettlementStatusUnknown:
		return true
	default:
		return false
	}
}

func (s SettlementStatus) String() string {
	if !s.Valid() {
		return invalidEnumName
	}
	return string(s)
}

// Settlement is the immutable final fact for one EffectID. Payload is owned by
// the Effect target and becomes opaque Signal data for the next Step. The
// Engine uses Status only to preserve definite versus unknown execution facts.
type Settlement struct {
	effectID EffectID
	status   SettlementStatus
	payload  json.RawMessage
}

func (s Settlement) clone() Settlement {
	s.payload = bytes.Clone(s.payload)
	return s
}

// NewSettlement binds a result to the exact effect identity it settles, so a
// dispatcher cannot close an effect other than the one it was given. Ordering
// by completion time instead would let a slow settlement overwrite a newer
// one.
func NewSettlement(effectID EffectID, status SettlementStatus, payload json.RawMessage) (Settlement, error) {
	if !effectID.Valid() {
		return Settlement{}, fmt.Errorf("%w: effect ID: %w", ErrInvalidSettlement, ErrInvalidIdentity)
	}
	if !status.Valid() {
		return Settlement{}, fmt.Errorf("%w: status is required", ErrInvalidSettlement)
	}
	normalized, err := wireJSON.normalize(payload, maxWireBytes)
	if err != nil {
		return Settlement{}, fmt.Errorf("%w: payload: %w", ErrInvalidSettlement, err)
	}
	return Settlement{effectID: effectID, status: status, payload: normalized}, nil
}

// EffectID returns the Effect this result settles.
func (s Settlement) EffectID() EffectID { return s.effectID }

// Status returns whether the external result is definite or unknown.
func (s Settlement) Status() SettlementStatus { return s.status }

// Payload returns an independently owned owner-defined result.
func (s Settlement) Payload() json.RawMessage { return bytes.Clone(s.payload) }

func (s Settlement) Valid() bool {
	return s.effectID.Valid() && s.status.Valid() && len(s.payload) > 0
}

func (s Settlement) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalidSettlement
	}
	return json.Marshal(settlementWire{EffectID: s.effectID, Status: s.status, Payload: s.payload})
}

func (s *Settlement) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidSettlement)
	}
	wire, err := wireJSON.decode[settlementWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidSettlement, err)
	}
	value, err := NewSettlement(wire.EffectID, wire.Status, wire.Payload)
	if err != nil {
		return err
	}
	*s = value
	return nil
}

func (s Settlement) equal(other Settlement) bool {
	return s.Valid() && other.Valid() && s.effectID == other.effectID &&
		s.status == other.status && bytes.Equal(s.payload, other.payload)
}

type settlementWire struct {
	EffectID EffectID         `json:"effect_id"`
	Status   SettlementStatus `json:"status"`
	Payload  json.RawMessage  `json:"payload"`
}
