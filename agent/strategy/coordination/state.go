package coordination

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

var (
	ErrInvalidConfig   = errors.New("coordination: invalid configuration")
	ErrInvalidState    = errors.New("coordination: invalid execution state")
	ErrInvalidProtocol = errors.New("coordination: invalid execution protocol")
)

func encodeState[T any](kind string, state T) (agent.ExecutionState, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return agent.ExecutionState{}, fmt.Errorf("%w: encode: %w", ErrInvalidState, err)
	}
	return agent.NewExecutionState(kind, payload)
}

func decodeState[T any](kind string, state agent.ExecutionState) (T, error) {
	var decoded T
	if state.Kind() != kind {
		return decoded, fmt.Errorf("%w: unexpected state kind", ErrInvalidState)
	}
	if err := jsonv2.Unmarshal(state.Payload(), &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return decoded, fmt.Errorf("%w: decode: %w", ErrInvalidState, err)
	}
	return decoded, nil
}
