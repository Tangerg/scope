package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

type operation string

const (
	operationSense  operation = "sense"
	operationAction operation = "action"
)

func (o operation) valid() bool { return o == operationSense || o == operationAction }

type effectEnvelope struct {
	Operation operation     `json:"operation"`
	Input     agent.Payload `json:"input"`
	Action    *actionCall   `json:"action,omitempty"`
}

type actionCall struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	WorldState  WorldState `json:"world_state"`
}

type signalEnvelope struct {
	HostError string            `json:"host_error,omitempty"`
	Operation operation         `json:"operation,omitempty"`
	Sensing   *senseResult      `json:"sensing,omitempty"`
	Action    *actionResultWire `json:"action,omitempty"`
}

type senseResult struct {
	WorldState *WorldState `json:"world_state,omitempty"`
	Error      string      `json:"error,omitempty"`
}

type actionResultWire struct {
	Succeeded  bool   `json:"succeeded"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

func newSenseEffect(input agent.Payload) (agent.Effect, error) {
	if !input.Valid() {
		return agent.Effect{}, ErrInvalidProtocol
	}
	payload, err := jsonv2.Marshal(effectEnvelope{
		Operation: operationSense, Input: input,
	}, jsonv2.Deterministic(true))
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
		Operation: operationAction,
		Input:     input,
		Action: &actionCall{
			Name: binding.action.name, Description: binding.action.description, WorldState: state,
		},
	}, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Effect{}, err
	}
	return agent.NewDispatcherEffect(payload, binding.required.Values()...)
}

func decodeEffect(payload json.RawMessage) (effectEnvelope, error) {
	var envelope effectEnvelope
	if err := jsonv2.Unmarshal(payload, &envelope, jsonv2.RejectUnknownMembers(true)); err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: decode Effect: %w", ErrInvalidProtocol, err)
	}
	if !envelope.Operation.valid() || !envelope.Input.Valid() {
		return effectEnvelope{}, ErrInvalidProtocol
	}
	switch envelope.Operation {
	case operationSense:
		if envelope.Action != nil {
			return effectEnvelope{}, ErrInvalidProtocol
		}
	case operationAction:
		if envelope.Action == nil || !agent.ValidQualifiedName(envelope.Action.Name) ||
			!agent.ValidDescription(envelope.Action.Description) {
			return effectEnvelope{}, ErrInvalidProtocol
		}
	}
	return envelope, nil
}

func senseSignal(state WorldState, cause error) (json.RawMessage, error) {
	result := &senseResult{}
	if cause != nil {
		result.Error = agent.NormalizeDiagnostic(cause.Error())
	} else {
		cloned := state
		result.WorldState = &cloned
	}
	return jsonv2.Marshal(signalEnvelope{
		Operation: operationSense, Sensing: result,
	}, jsonv2.Deterministic(true))
}

func actionSignal(result ActionResult) (json.RawMessage, error) {
	if !result.Valid() {
		return nil, ErrInvalidProtocol
	}
	return jsonv2.Marshal(signalEnvelope{
		Operation: operationAction,
		Action: &actionResultWire{
			Succeeded: result.Succeeded(), Diagnostic: result.Diagnostic(),
		},
	}, jsonv2.Deterministic(true))
}

func decodeSignal(payload json.RawMessage) (signalEnvelope, error) {
	var envelope signalEnvelope
	if err := jsonv2.Unmarshal(payload, &envelope, jsonv2.RejectUnknownMembers(true)); err != nil {
		return signalEnvelope{}, fmt.Errorf("%w: decode Signal: %w", ErrInvalidProtocol, err)
	}
	if envelope.HostError != "" {
		if !agent.ValidDiagnostic(envelope.HostError) || envelope.Operation != "" || envelope.Sensing != nil || envelope.Action != nil {
			return signalEnvelope{}, ErrInvalidProtocol
		}
		return envelope, nil
	}
	if !envelope.Operation.valid() {
		return signalEnvelope{}, ErrInvalidProtocol
	}
	switch envelope.Operation {
	case operationSense:
		if envelope.Sensing == nil || envelope.Action != nil ||
			(envelope.Sensing.WorldState == nil) == (envelope.Sensing.Error == "") {
			return signalEnvelope{}, ErrInvalidProtocol
		}
		if envelope.Sensing.Error != "" && !agent.ValidDiagnostic(envelope.Sensing.Error) {
			return signalEnvelope{}, ErrInvalidProtocol
		}
	case operationAction:
		if envelope.Action == nil || envelope.Sensing != nil {
			return signalEnvelope{}, ErrInvalidProtocol
		}
		if envelope.Action.Succeeded && envelope.Action.Diagnostic != "" ||
			!envelope.Action.Succeeded && !agent.ValidDiagnostic(envelope.Action.Diagnostic) {
			return signalEnvelope{}, ErrInvalidProtocol
		}
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
