package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidChildControl = errors.New("agent: invalid child control")

// NewChildSignalEffect declares delivery through the child's ordinary mailbox contract.
// It cannot release an unrelated wait or preempt in-flight external work.
// Delivery and the parent Effect settlement share one tree acknowledgment.
// The exact SignalRequest retains its caller-chosen deduplication identity.
// Cross-tree delivery remains a Host-authorized messaging operation.
func NewChildSignalEffect(childID ProcessID, signal SignalRequest) (Effect, error) {
	if !childID.Valid() || !signal.Valid() {
		return Effect{}, ErrInvalidChildControl
	}
	return childControlEffectWire{
		Operation: frameworkOperationSignalChild, ChildID: childID, Signal: &signal,
	}.effect()
}

// NewChildCancelEffect declares cancellation of an exact direct child's subtree.
// The receipt confirms the recorded intent, not termination or resource release;
// use a drained child wait before reusing exclusive resources. An already
// terminal direct child succeeds without changing its result.
func NewChildCancelEffect(childID ProcessID, reason string) (Effect, error) {
	if !childID.Valid() || validateTerminationReason(reason) != nil {
		return Effect{}, ErrInvalidChildControl
	}
	return childControlEffectWire{
		Operation: frameworkOperationCancelChild, ChildID: childID, Reason: reason,
	}.effect()
}

// ChildControlResult is a definite tree-local admission result. Failure
// preserves rejected authority, wait, or resource admission without failing
// the sending Strategy implicitly. The declaring Effect owns the recipient and
// any delivered SignalID; the result carries only whether admission failed.
type ChildControlResult struct {
	failure   Failure
	operation frameworkOperationKind
}

func (c ChildControlResult) Failure() (Failure, bool) { return c.failure, c.failure.Valid() }

// settlementStatus is the status of the child-control Effect that returns c.
func (c ChildControlResult) settlementStatus() SettlementStatus {
	if c.failure.Valid() {
		return SettlementStatusFailed
	}
	return SettlementStatusSucceeded
}

func (c ChildControlResult) Valid() bool {
	return c.operation == frameworkOperationSignalChild || c.operation == frameworkOperationCancelChild
}

func (c ChildControlResult) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, ErrInvalidChildControl
	}
	wire := childControlResultWire{Operation: c.operation}
	if c.failure.Valid() {
		wire.Failure = &c.failure
	}
	return jsonv2.Marshal(wire)
}

func (c *ChildControlResult) UnmarshalJSON(data []byte) error {
	if c == nil {
		return ErrInvalidChildControl
	}
	value, err := decodeChildControlResult(data)
	if err != nil {
		return err
	}
	*c = value
	return nil
}

func (ChildControlResult) JSONSchemaAlias() any { return childControlResultWire{} }

// ParseChildControlResult decodes an unaddressed Framework control settlement
// carrying an Engine-owned Signal identity.
func ParseChildControlResult(signal Signal) (ChildControlResult, error) {
	if !signal.EngineOwned() {
		return ChildControlResult{}, ErrInvalidSignal
	}
	if _, addressed := signal.WaitID(); addressed {
		return ChildControlResult{}, ErrInvalidChildControl
	}
	return decodeChildControlResult(signal.Payload())
}

type childControlEffectWire struct {
	Operation frameworkOperationKind `json:"operation"`
	ChildID   ProcessID              `json:"child_id"`
	Signal    *SignalRequest         `json:"signal,omitzero"`
	Reason    string                 `json:"reason,omitempty"`
}

func (c childControlEffectWire) valid() bool {
	if !c.ChildID.Valid() {
		return false
	}
	switch c.Operation {
	case frameworkOperationSignalChild:
		return c.Signal != nil && c.Signal.Valid() && c.Reason == ""
	case frameworkOperationCancelChild:
		return c.Signal == nil && validateTerminationReason(c.Reason) == nil
	default:
		return false
	}
}

func (c childControlEffectWire) effect() (Effect, error) {
	if !c.valid() {
		return Effect{}, ErrInvalidChildControl
	}
	return newFrameworkEffect(c)
}

func decodeChildControlEffect(payload json.RawMessage) (childControlEffectWire, error) {
	wire, err := jsonwire.Decode[childControlEffectWire](payload)
	if err != nil {
		return childControlEffectWire{}, fmt.Errorf("%w: effect: %w", ErrInvalidChildControl, err)
	}
	if !wire.valid() {
		return childControlEffectWire{}, ErrInvalidChildControl
	}
	return wire, nil
}

type childControlResultWire struct {
	Operation frameworkOperationKind `json:"operation"`
	Failure   *Failure               `json:"failure,omitzero"`
}

func decodeChildControlResult(payload json.RawMessage) (ChildControlResult, error) {
	wire, err := jsonwire.Decode[childControlResultWire](payload)
	if err != nil {
		return ChildControlResult{}, fmt.Errorf("%w: result: %w", ErrInvalidChildControl, err)
	}
	result := ChildControlResult{operation: wire.Operation}
	if wire.Failure != nil {
		result.failure = *wire.Failure
	}
	if !result.Valid() {
		return ChildControlResult{}, ErrInvalidChildControl
	}
	return result, nil
}

func (c childControlEffectWire) result() ChildControlResult {
	return ChildControlResult{operation: c.Operation}
}
