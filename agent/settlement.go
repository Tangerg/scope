package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidSettlement = errors.New("agent: invalid effect settlement")

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
// success flag. The Engine delivers a definite Dispatcher settlement to the
// next Step, where ParseSettlement reads both.
type Settlement struct {
	status  SettlementStatus
	payload json.RawMessage
}

func (s Settlement) clone() Settlement {
	s.payload = bytes.Clone(s.payload)
	return s
}

func NewSettlement(status SettlementStatus, payload json.RawMessage) (Settlement, error) {
	if !status.Valid() {
		return Settlement{}, fmt.Errorf("%w: status is required", ErrInvalidSettlement)
	}
	normalized, err := normalizeJSON(payload, MaxPayloadBytes)
	if err != nil {
		return Settlement{}, fmt.Errorf("%w: payload: %w", ErrInvalidSettlement, err)
	}
	return Settlement{status: status, payload: normalized}, nil
}

func (s Settlement) Status() SettlementStatus { return s.status }

// Payload returns an independently owned owner-defined result.
func (s Settlement) Payload() json.RawMessage { return bytes.Clone(s.payload) }

func (s Settlement) Valid() bool {
	return s.status.Valid() && len(s.payload) > 0
}

// definite reports a settlement the Engine delivers to the Execution: only an
// unknown outcome stays with the Host until it is resolved.
func (s Settlement) definite() bool {
	return s.Valid() && s.status != SettlementStatusUnknown
}

// ParseSettlement reads a Dispatcher settlement delivered to an Execution.
// Its Status owns whether the Effect succeeded; its Payload carries the
// Dispatcher's result or diagnostic.
func ParseSettlement(signal Signal) (Settlement, error) {
	if signal.status == SettlementStatusInvalid {
		return Settlement{}, fmt.Errorf("%w: Signal does not deliver a Dispatcher settlement", ErrInvalidSettlement)
	}
	return Settlement{status: signal.status, payload: bytes.Clone(signal.payload)}, nil
}

func (s Settlement) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalidSettlement
	}
	return jsonv2.Marshal(settlementWire{Status: s.status, Payload: s.payload})
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

func (s Settlement) equal(other Settlement) bool {
	return s.Valid() && other.Valid() && s.status == other.status && bytes.Equal(s.payload, other.payload)
}

type settlementWire struct {
	Status  SettlementStatus `json:"status"`
	Payload json.RawMessage  `json:"payload"`
}
