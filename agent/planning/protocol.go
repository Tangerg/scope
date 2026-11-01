package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/agent/run"
)

type operation string

const (
	operationSense  operation = "sense"
	operationAction operation = "action"
)

// effectEnvelope is a sense request unless it names an Action.
type effectEnvelope struct {
	Input  run.Payload `json:"input"`
	Action *actionCall `json:"action,omitzero"`
}

func (e effectEnvelope) operation() operation {
	if e.Action != nil {
		return operationAction
	}
	return operationSense
}

// actionCall names the bound Action; the binding owns its description.
type actionCall struct {
	Name       string     `json:"name"`
	WorldState WorldState `json:"world_state"`
}

// The settlement status owns whether an operation succeeded. A successful
// sensing settlement carries the observed world state and a successful Action
// settlement carries nothing; a failed settlement carries settlementFailure.
type senseResult struct {
	WorldState WorldState `json:"world_state" jsonwire:"required"`
}

type actionCompleted struct{}

// settlementFailure explains a failed settlement: either the host rejected
// the operation, or the operation itself definitely failed.
type settlementFailure struct {
	HostError  string `json:"host_error,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

func (s settlementFailure) valid() bool {
	if s.HostError != "" {
		return s.Diagnostic == "" && run.ValidDiagnostic(s.HostError)
	}
	return run.ValidDiagnostic(s.Diagnostic)
}

func newSenseEffect(input run.Payload) (run.Effect, error) {
	if !input.Valid() {
		return run.Effect{}, ErrInvalidProtocol
	}
	payload, err := jsonv2.Marshal(effectEnvelope{Input: input}, jsonv2.Deterministic(true))
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

func newActionEffect(input run.Payload, binding ActionBinding, state WorldState) (run.Effect, error) {
	if !input.Valid() || !binding.Valid() || binding.delegatesToChild() ||
		!binding.action.Applicable(state) {
		return run.Effect{}, ErrInvalidProtocol
	}
	payload, err := jsonv2.Marshal(effectEnvelope{
		Input: input,
		Action: &actionCall{
			Name: binding.action.name, WorldState: state,
		},
	}, jsonv2.Deterministic(true))
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

func decodeEffect(payload json.RawMessage) (effectEnvelope, error) {
	envelope, err := jsonwire.Decode[effectEnvelope](payload)
	if err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: decode Effect: %w", ErrInvalidProtocol, err)
	}
	if !envelope.valid() {
		return effectEnvelope{}, ErrInvalidProtocol
	}
	return envelope, nil
}

func (e effectEnvelope) valid() bool {
	if !e.Input.Valid() {
		return false
	}
	return e.Action == nil || run.ValidQualifiedName(e.Action.Name)
}

func senseSettlement(state WorldState, cause error) (run.Settlement, error) {
	if cause != nil {
		return failedSettlement(settlementFailure{Diagnostic: run.NormalizeDiagnostic(cause.Error())})
	}
	payload, err := jsonv2.Marshal(senseResult{WorldState: state}, jsonv2.Deterministic(true))
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload)
}

func failedSettlement(failure settlementFailure) (run.Settlement, error) {
	payload, err := jsonv2.Marshal(failure, jsonv2.Deterministic(true))
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusFailed, payload)
}

// decodeSettlement reads the single Dispatcher settlement of the awaited
// operation. A failed settlement returns its explanation instead of a result.
func decodeSettlement[T any](signals []run.Signal) (T, *settlementFailure, error) {
	var result T
	// ParseSettlement owns the settlement's Runtime origin.
	if len(signals) != 1 {
		return result, nil, fmt.Errorf("%w: exactly one settlement Signal is required", ErrInvalidProtocol)
	}
	settlement, err := run.ParseSettlement(signals[0])
	if err != nil {
		return result, nil, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	if settlement.Status() == run.SettlementStatusFailed {
		failure, decodeErr := jsonwire.Decode[settlementFailure](settlement.Payload())
		if decodeErr != nil || !failure.valid() {
			return result, nil, fmt.Errorf("%w: invalid settlement failure", ErrInvalidProtocol)
		}
		return result, &failure, nil
	}
	if result, err = jsonwire.Decode[T](settlement.Payload()); err != nil {
		return result, nil, fmt.Errorf("%w: decode settlement: %w", ErrInvalidProtocol, err)
	}
	return result, nil, nil
}
