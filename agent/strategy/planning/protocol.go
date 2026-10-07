package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

type operation string

const (
	operationSense  operation = "sense"
	operationAction operation = "action"
)

// effectEnvelope is a sense request unless it names an Action.
type effectEnvelope struct {
	Input  agent.Payload `json:"input"`
	Action *actionCall   `json:"action,omitzero"`
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
		return s.Diagnostic == "" && agent.ValidDiagnostic(s.HostError)
	}
	return agent.ValidDiagnostic(s.Diagnostic)
}

func newSenseEffect(input agent.Payload) (agent.Effect, error) {
	if !input.Valid() {
		return agent.Effect{}, ErrInvalidProtocol
	}
	payload, err := jsonv2.Marshal(effectEnvelope{Input: input}, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Effect{}, err
	}
	return agent.NewDispatcherEffect(payload)
}

func newActionEffect(input agent.Payload, binding ActionBinding, state WorldState) (agent.Effect, error) {
	if !input.Valid() || !binding.Valid() || binding.delegatesToChild() ||
		!binding.action.Applicable(state) {
		return agent.Effect{}, ErrInvalidProtocol
	}
	payload, err := jsonv2.Marshal(effectEnvelope{
		Input: input,
		Action: &actionCall{
			Name: binding.action.name, WorldState: state,
		},
	}, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Effect{}, err
	}
	return agent.NewDispatcherEffect(payload)
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
	return e.Action == nil || agent.ValidQualifiedName(e.Action.Name)
}

func senseSettlement(state WorldState, cause error) (agent.Settlement, error) {
	if cause != nil {
		return failedSettlement(settlementFailure{Diagnostic: agent.NormalizeDiagnostic(cause.Error())})
	}
	payload, err := jsonv2.Marshal(senseResult{WorldState: state}, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(agent.SettlementStatusSucceeded, payload)
}

func failedSettlement(failure settlementFailure) (agent.Settlement, error) {
	payload, err := jsonv2.Marshal(failure, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(agent.SettlementStatusFailed, payload)
}

// decodeSettlement reads the single Dispatcher settlement of the awaited
// operation. A failed settlement returns its explanation instead of a result.
func decodeSettlement[T any](signals []agent.Signal) (T, *settlementFailure, error) {
	var result T
	signal, err := oneSignal(signals)
	if err != nil {
		return result, nil, err
	}
	settlement, err := agent.ParseSettlement(signal)
	if err != nil {
		return result, nil, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	if settlement.Status() == agent.SettlementStatusFailed {
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

func oneSignal(signals []agent.Signal) (agent.Signal, error) {
	if len(signals) != 1 || !signals[0].EngineOwned() {
		return agent.Signal{}, fmt.Errorf("%w: exactly one Engine-owned settlement Signal is required", ErrInvalidProtocol)
	}
	if _, addressed := signals[0].WaitID(); addressed {
		return agent.Signal{}, fmt.Errorf("%w: dispatcher settlement Signal must not address a wait", ErrInvalidProtocol)
	}
	return signals[0], nil
}
