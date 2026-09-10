package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
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
	Operation operation   `json:"operation"`
	Input     agent.Input `json:"input"`
	Action    *actionCall `json:"action,omitempty"`
}

type actionCall struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	WorldState  WorldState `json:"world_state"`
}

type signalEnvelope struct {
	Operation operation         `json:"operation"`
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

func newSenseEffect(input agent.Input) (agent.Effect, error) {
	if !input.Valid() {
		return agent.Effect{}, ErrInvalidProtocol
	}
	payload, err := encodeProtocol(effectEnvelope{
		Operation: operationSense, Input: input,
	})
	if err != nil {
		return agent.Effect{}, err
	}
	return agent.NewDispatcherEffect(payload)
}

func newActionEffect(input agent.Input, binding ActionBinding, state WorldState) (agent.Effect, error) {
	if !input.Valid() || !binding.Valid() || binding.target != bindingTargetDispatcher || !state.Valid() ||
		!binding.action.Applicable(state) {
		return agent.Effect{}, ErrInvalidProtocol
	}
	payload, err := encodeProtocol(effectEnvelope{
		Operation: operationAction,
		Input:     input,
		Action: &actionCall{
			Name: binding.action.name, Description: binding.action.description, WorldState: state,
		},
	})
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
		if envelope.Action == nil || !validName(envelope.Action.Name) ||
			!validDescription(envelope.Action.Description) || !envelope.Action.WorldState.Valid() {
			return effectEnvelope{}, ErrInvalidProtocol
		}
	}
	return envelope, nil
}

func senseSignal(state WorldState, cause error) (json.RawMessage, error) {
	result := &senseResult{}
	if cause != nil {
		result.Error = diagnostic(cause.Error())
	} else {
		if !state.Valid() {
			return nil, ErrInvalidProtocol
		}
		cloned := state
		result.WorldState = &cloned
	}
	return encodeProtocol(signalEnvelope{
		Operation: operationSense, Sensing: result,
	})
}

func actionSignal(result ActionResult) (json.RawMessage, error) {
	if !result.Valid() {
		return nil, ErrInvalidProtocol
	}
	return encodeProtocol(signalEnvelope{
		Operation: operationAction,
		Action: &actionResultWire{
			Succeeded: result.Succeeded(), Diagnostic: result.Diagnostic(),
		},
	})
}

func decodeSignal(payload json.RawMessage) (signalEnvelope, error) {
	var envelope signalEnvelope
	if err := jsonv2.Unmarshal(payload, &envelope, jsonv2.RejectUnknownMembers(true)); err != nil {
		return signalEnvelope{}, fmt.Errorf("%w: decode Signal: %w", ErrInvalidProtocol, err)
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
		if envelope.Sensing.WorldState != nil && !envelope.Sensing.WorldState.Valid() ||
			envelope.Sensing.Error != "" && diagnostic(envelope.Sensing.Error) != envelope.Sensing.Error {
			return signalEnvelope{}, ErrInvalidProtocol
		}
	case operationAction:
		if envelope.Action == nil || envelope.Sensing != nil {
			return signalEnvelope{}, ErrInvalidProtocol
		}
		if envelope.Action.Succeeded && envelope.Action.Diagnostic != "" ||
			!envelope.Action.Succeeded && (envelope.Action.Diagnostic == "" ||
				diagnostic(envelope.Action.Diagnostic) != envelope.Action.Diagnostic) {
			return signalEnvelope{}, ErrInvalidProtocol
		}
	}
	return envelope, nil
}

func encodeProtocol(value any) (json.RawMessage, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %w", ErrInvalidProtocol, err)
	}
	return payload, nil
}

func oneSignal(signals []agent.Signal) (agent.Signal, error) {
	if len(signals) != 1 || !signals[0].Valid() {
		return agent.Signal{}, errors.New("planning: exactly one valid settlement Signal is required")
	}
	if _, addressed := signals[0].WaitID(); addressed {
		return agent.Signal{}, errors.New("planning: dispatcher settlement Signal must not address a wait")
	}
	return signals[0], nil
}
