package coordination

import (
	"encoding/json"
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
