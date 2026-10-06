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

// signalEnvelope carries exactly one of a host rejection, a sensing result,
// and an Action result; the member present names the operation it settles.
type signalEnvelope struct {
	HostError string            `json:"host_error,omitempty"`
	Sensing   *senseResult      `json:"sensing,omitzero"`
	Action    *actionResultWire `json:"action,omitzero"`
}

func (s signalEnvelope) operation() operation {
	if s.Action != nil {
		return operationAction
	}
	return operationSense
}

type senseResult struct {
	WorldState *WorldState `json:"world_state,omitzero"`
	Error      string      `json:"error,omitempty"`
}

func (s senseResult) valid() bool {
	if s.Error != "" {
		return s.WorldState == nil && agent.ValidDiagnostic(s.Error)
	}
	return s.WorldState != nil
}

// actionResultWire reports a failure exactly by its diagnostic.
type actionResultWire struct {
	Diagnostic string `json:"diagnostic,omitempty"`
}

func (a actionResultWire) result() ActionResult {
	return ActionResult{succeeded: a.Diagnostic == "", diagnostic: a.Diagnostic}
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
	if !input.Valid() || !binding.Valid() || binding.target != bindingTargetDispatcher ||
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

func senseSignal(state WorldState, cause error) (json.RawMessage, error) {
	result := &senseResult{}
	if cause != nil {
		result.Error = agent.NormalizeDiagnostic(cause.Error())
	} else {
		cloned := state
		result.WorldState = &cloned
	}
	return jsonv2.Marshal(signalEnvelope{Sensing: result}, jsonv2.Deterministic(true))
}

func actionSignal(result ActionResult) (json.RawMessage, error) {
	if !result.Valid() {
		return nil, ErrInvalidProtocol
	}
	return jsonv2.Marshal(signalEnvelope{
		Action: &actionResultWire{Diagnostic: result.Diagnostic()},
	}, jsonv2.Deterministic(true))
}

func decodeSignal(payload json.RawMessage) (signalEnvelope, error) {
	envelope, err := jsonwire.Decode[signalEnvelope](payload)
	if err != nil {
		return signalEnvelope{}, fmt.Errorf("%w: decode Signal: %w", ErrInvalidProtocol, err)
	}
	if !envelope.valid() {
		return signalEnvelope{}, ErrInvalidProtocol
	}
	return envelope, nil
}

func (s signalEnvelope) valid() bool {
	switch {
	case s.HostError != "":
		return agent.ValidDiagnostic(s.HostError) && s.Sensing == nil && s.Action == nil
	case s.Sensing != nil:
		return s.Action == nil && s.Sensing.valid()
	case s.Action != nil:
		return s.Action.result().Valid()
	default:
		return false
	}
}

// decodeSettlement accepts the single dispatcher settlement for expected, or
// a host_error rejection of that operation.
func decodeSettlement(signals []agent.Signal, expected operation) (signalEnvelope, error) {
	signal, err := oneSignal(signals)
	if err != nil {
		return signalEnvelope{}, err
	}
	envelope, err := decodeSignal(signal.Payload())
	if err != nil {
		return signalEnvelope{}, fmt.Errorf("%w: expected %s Signal: %w", ErrInvalidProtocol, expected, err)
	}
	if envelope.HostError == "" && envelope.operation() != expected {
		return signalEnvelope{}, fmt.Errorf("%w: expected %s Signal", ErrInvalidProtocol, expected)
	}
	return envelope, nil
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
