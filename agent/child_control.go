package agent

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidChildControl = errors.New("agent: invalid child control")

// SignalChild declares delivery through the child's ordinary mailbox contract.
// It cannot release an unrelated wait or preempt in-flight external work.
// Delivery and the parent Effect settlement share one tree acknowledgment.
// The exact SignalRequest retains its caller-chosen deduplication identity.
// Cross-tree delivery remains a Host-authorized messaging operation.
func SignalChild(childID ProcessID, signal SignalRequest) (Effect, error) {
	if !childID.Valid() || !signal.Valid() {
		return Effect{}, ErrInvalidChildControl
	}
	return childControlEffectWire{
		Operation: frameworkEffectSignalChild, ChildID: childID, Signal: &signal,
	}.effect()
}

// CancelChild declares cancellation of an exact direct child's subtree.
// The receipt confirms the recorded intent, not termination or resource release;
// use a drained child wait before reusing exclusive resources. An already
// terminal direct child succeeds without changing its result.
func CancelChild(childID ProcessID, reason string) (Effect, error) {
	if !childID.Valid() || validateTerminationReason(reason) != nil {
		return Effect{}, ErrInvalidChildControl
	}
	return childControlEffectWire{
		Operation: frameworkEffectCancelChild, ChildID: childID, Reason: reason,
	}.effect()
}

// ChildControlResult is a definite tree-local admission result. SignalID is
// present for every signal attempt, including rejected delivery. Failure
// preserves rejected authority, wait, or resource admission without failing
// the sending Strategy implicitly.
type ChildControlResult struct {
	childID   ProcessID
	signalID  SignalID
	failure   Failure
	operation frameworkEffectOperation
}

func (c ChildControlResult) ChildID() ProcessID { return c.childID }

func (c ChildControlResult) SignalID() (SignalID, bool) {
	return c.signalID, c.signalID.Valid()
}

func (c ChildControlResult) Failure() (Failure, bool) { return c.failure, c.failure.Valid() }

// Matches checks the operation and concrete recipient of the declared Effect.
// The enclosing settlement Signal retains the Engine-owned effect identity.
func (c ChildControlResult) Matches(effect Effect) bool {
	if !c.Valid() || !effect.Valid() || effect.Target() != EffectTargetFramework {
		return false
	}
	request, err := decodeChildControlEffect(effect.Payload())
	return err == nil && request.Operation == c.operation && request.ChildID == c.childID &&
		(!c.signalID.Valid() || request.Signal != nil && request.Signal.ID() == c.signalID)
}

func (c ChildControlResult) Valid() bool {
	if !c.childID.Valid() || c.operation != frameworkEffectSignalChild && c.operation != frameworkEffectCancelChild {
		return false
	}
	return c.signalID.Valid() == (c.operation == frameworkEffectSignalChild)
}

func (c ChildControlResult) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, ErrInvalidChildControl
	}
	wire := childControlResultWire{Operation: c.operation, ChildID: c.childID}
	if c.signalID.Valid() {
		wire.SignalID = &c.signalID
	}
	if c.failure.Valid() {
		wire.Failure = &c.failure
	}
	return json.Marshal(wire)
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

// ParseChildControlResult decodes an unaddressed Framework control settlement.
func ParseChildControlResult(signal Signal) (ChildControlResult, error) {
	if !signal.Valid() {
		return ChildControlResult{}, ErrInvalidSignal
	}
	if _, addressed := signal.WaitID(); addressed {
		return ChildControlResult{}, ErrInvalidChildControl
	}
	return decodeChildControlResult(signal.Payload())
}

type childControlEffectWire struct {
	Operation frameworkEffectOperation `json:"operation"`
	ChildID   ProcessID                `json:"child_id"`
	Signal    *SignalRequest           `json:"signal,omitempty"`
	Reason    string                   `json:"reason,omitempty"`
}

func (c childControlEffectWire) valid() bool {
	if !c.ChildID.Valid() {
		return false
	}
	switch c.Operation {
	case frameworkEffectSignalChild:
		return c.Signal != nil && c.Signal.Valid() && c.Reason == ""
	case frameworkEffectCancelChild:
		return c.Signal == nil && validateTerminationReason(c.Reason) == nil
	default:
		return false
	}
}

func (c childControlEffectWire) effect() (Effect, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return Effect{}, err
	}
	return newEffect(EffectTargetFramework, payload)
}

func decodeChildControlEffect(payload json.RawMessage) (childControlEffectWire, error) {
	wire, err := wireJSON.decode[childControlEffectWire](payload)
	if err != nil {
		return childControlEffectWire{}, fmt.Errorf("%w: effect: %w", ErrInvalidChildControl, err)
	}
	if !wire.valid() {
		return childControlEffectWire{}, ErrInvalidChildControl
	}
	return wire, nil
}

type childControlResultWire struct {
	Operation frameworkEffectOperation `json:"operation"`
	ChildID   ProcessID                `json:"child_id"`
	SignalID  *SignalID                `json:"signal_id,omitempty"`
	Failure   *Failure                 `json:"failure,omitempty"`
}

func decodeChildControlResult(payload json.RawMessage) (ChildControlResult, error) {
	wire, err := wireJSON.decode[childControlResultWire](payload)
	if err != nil {
		return ChildControlResult{}, fmt.Errorf("%w: result: %w", ErrInvalidChildControl, err)
	}
	result := ChildControlResult{childID: wire.ChildID, operation: wire.Operation}
	if wire.SignalID != nil {
		result.signalID = *wire.SignalID
	}
	if wire.Failure != nil {
		result.failure = *wire.Failure
	}
	if !result.Valid() {
		return ChildControlResult{}, ErrInvalidChildControl
	}
	return result, nil
}

func (c childControlEffectWire) result() ChildControlResult {
	result := ChildControlResult{childID: c.ChildID, operation: c.Operation}
	if c.Signal != nil {
		result.signalID = c.Signal.ID()
	}
	return result
}
