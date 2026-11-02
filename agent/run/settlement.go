package run

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidSettlement = errors.New("run: invalid effect settlement")

// SettlementStatus records whether an Effect definitely succeeded, definitely
// failed, or has an unknown external result. Unknown never implies safe retry.
type SettlementStatus string

const (
	SettlementStatusInvalid   SettlementStatus = ""
	SettlementStatusSucceeded SettlementStatus = "succeeded"
	SettlementStatusFailed    SettlementStatus = "failed"
	SettlementStatusUnknown   SettlementStatus = "unknown"
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

// Settlement is the immutable final fact for one Effect. The Effect it answers
// owns the EffectID, so a Settlement is addressed by where it is returned or
// recorded rather than by a copy of that identity. Status owns whether the
// Effect definitely succeeded, definitely failed, or remains unknown; a
// Dispatcher payload carries only the result or diagnostic, never a second
// success flag. The Runtime delivers a definite Dispatcher settlement to the
// next Step as a Completion message.
type Settlement struct {
	status  SettlementStatus
	payload Payload
}

func NewSettlement(status SettlementStatus, payload json.RawMessage) (Settlement, error) {
	if !status.Valid() {
		return Settlement{}, fmt.Errorf("%w: status is required", ErrInvalidSettlement)
	}
	value, err := ParsePayload(payload)
	if err != nil {
		return Settlement{}, fmt.Errorf("%w: payload: %w", ErrInvalidSettlement, err)
	}
	return Settlement{status: status, payload: value}, nil
}

func (s Settlement) Status() SettlementStatus { return s.status }

// Payload is the owner-defined result; its Decode reads the owner's type.
func (s Settlement) Payload() Payload { return s.payload }

func (s Settlement) Valid() bool {
	return s.status.Valid() && s.payload.Valid()
}

func (s Settlement) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalidSettlement
	}
	return jsonv2.Marshal(settlementWire{Status: s.status, Payload: s.payload.JSON()})
}

func (s *Settlement) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidSettlement)
	}
	wire, err := jsonwire.Decode[settlementWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidSettlement, err)
	}
	value, err := NewSettlement(wire.Status, wire.Payload)
	if err != nil {
		return err
	}
	*s = value
	return nil
}

func (Settlement) JSONSchemaModel() any { return settlementWire{} }

type settlementWire struct {
	Status  SettlementStatus `json:"status"`
	Payload json.RawMessage  `json:"payload"`
}
