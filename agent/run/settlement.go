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
// Effect definitely succeeded, definitely failed, or remains unknown.
// Failure owns the diagnostic of a definite failure. Payload carries optional
// domain evidence independently, including work completed before a rejection.
// The Runtime delivers a definite settlement as a Completion message.
type Settlement struct {
	status  SettlementStatus
	payload Payload
	failure Failure
}

func NewSettlement(status SettlementStatus, payload json.RawMessage) (Settlement, error) {
	if status == SettlementStatusFailed {
		return Settlement{}, fmt.Errorf("%w: failed settlements require a Failure", ErrInvalidSettlement)
	}
	return admitSettlement(status, payload, nil)
}

func NewFailedSettlement(failure Failure, details Payload) (Settlement, error) {
	if !failure.Valid() {
		return Settlement{}, ErrInvalidFailure
	}
	return Settlement{status: SettlementStatusFailed, payload: details, failure: failure}, nil
}

func admitSettlement(status SettlementStatus, payload json.RawMessage, failure *Failure) (Settlement, error) {
	if !status.Valid() {
		return Settlement{}, fmt.Errorf("%w: status is required", ErrInvalidSettlement)
	}
	var value Payload
	if status != SettlementStatusFailed || len(payload) != 0 {
		var err error
		value, err = ParsePayload(payload)
		if err != nil {
			return Settlement{}, fmt.Errorf("%w: payload: %w", ErrInvalidSettlement, err)
		}
	}
	settlement := Settlement{status: status, payload: value}
	if failure != nil {
		settlement.failure = *failure
	}
	if !settlement.Valid() {
		return Settlement{}, ErrInvalidSettlement
	}
	return settlement, nil
}

func (s Settlement) Status() SettlementStatus { return s.status }

// Payload is domain evidence. A failed settlement may omit it; its diagnostic
// remains available through Failure without interpreting the domain's schema.
func (s Settlement) Payload() Payload { return s.payload }

func (s Settlement) Failure() (Failure, bool) {
	if s.status != SettlementStatusFailed {
		return Failure{}, false
	}
	return s.failure, s.failure.Valid()
}

func (s Settlement) Valid() bool {
	if s.status == SettlementStatusFailed {
		return s.failure.Valid()
	}
	return s.status.Valid() && s.payload.Valid() && !s.failure.Valid()
}

func (s Settlement) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalidSettlement
	}
	wire := settlementWire{Status: s.status, Payload: s.payload.JSON()}
	if s.status == SettlementStatusFailed {
		wire.Failure = new(s.failure)
	}
	return jsonv2.Marshal(wire)
}

func (s *Settlement) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidSettlement)
	}
	wire, err := jsonwire.Decode[settlementWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidSettlement, err)
	}
	value, err := admitSettlement(wire.Status, wire.Payload, wire.Failure)
	if err != nil {
		return err
	}
	*s = value
	return nil
}

func (Settlement) JSONSchemaModel() any { return settlementWire{} }

type settlementWire struct {
	Status  SettlementStatus `json:"status"`
	Payload json.RawMessage  `json:"payload,omitzero"`
	Failure *Failure         `json:"failure,omitzero"`
}

// Framework Effects settle with payloads the kernel defines: a Spawn names the
// started child, a Send or Cancel carries an empty object, and a rejection
// carries its Failure. These constructors and SpawnResult are their one codec,
// so strategies never depend on the encoding.
func frameworkSettlement(status SettlementStatus, value any) (Settlement, error) {
	payload, err := EncodePayload(value)
	if err != nil {
		return Settlement{}, err
	}
	return Settlement{status: status, payload: payload}, nil
}

func spawnedSettlement(child ProcessID) (Settlement, error) {
	return frameworkSettlement(SettlementStatusSucceeded, child)
}

func appliedSettlement() (Settlement, error) {
	return frameworkSettlement(SettlementStatusSucceeded, struct{}{})
}

// SpawnResult reads the outcome of a Spawn: the started child, or the Failure
// that rejected the start. Exactly one of them is valid.
func (s Settlement) SpawnResult() (ProcessID, Failure, error) {
	switch s.status {
	case SettlementStatusSucceeded:
		child, err := s.payload.Decode[ProcessID]()
		if err != nil || !child.Valid() {
			return ProcessID{}, Failure{}, fmt.Errorf("%w: spawn did not name its child", ErrInvalidSettlement)
		}
		return child, Failure{}, nil
	case SettlementStatusFailed:
		failure, valid := s.Failure()
		if !valid {
			return ProcessID{}, Failure{}, fmt.Errorf("%w: spawn rejection carries no failure", ErrInvalidSettlement)
		}
		return ProcessID{}, failure, nil
	default:
		return ProcessID{}, Failure{}, fmt.Errorf("%w: spawn outcome is not definite", ErrInvalidSettlement)
	}
}
