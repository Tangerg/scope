package interaction

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
)

// The admission shape excludes dispatcher results and Tool input responses.
type steerSignal struct {
	Steer steerInput `json:"steer"`
}

type steerInput struct {
	Messages []chat.Message `json:"messages"`
}

func (steerInput) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"messages": {MinItems: new(uint64(minimumSteeringMessages))},
	}}
}

type steerBatch struct {
	Messages  []chat.Message `json:"messages"`
	SignalIDs []run.SignalID `json:"signal_ids"`
}

func (s steerBatch) empty() bool {
	return len(s.Messages) == 0 && len(s.SignalIDs) == 0
}

func (s steerBatch) validate() error {
	if err := validateSteeringMessages(s.Messages); err != nil {
		return err
	}
	return validateSteerSignalIDs(s.SignalIDs)
}

func (s *steerBatch) appendSignal(signal run.Signal, messages []chat.Message) error {
	if signal.Wait.Valid() {
		return fmt.Errorf("%w: steer Signal must not address a wait", ErrInvalidSteer)
	}
	s.Messages = append(s.Messages, messages...)
	s.SignalIDs = append(s.SignalIDs, signal.ID)
	return nil
}

func (s *steerBatch) collectSignal(message run.Message) (bool, error) {
	signal, ok := message.(run.Signal)
	if !ok || signal.Wait.Valid() {
		return false, nil
	}
	return true, s.collectInput(signal)
}

func (s *steerBatch) collectInput(signal run.Signal) error {
	envelope, err := signal.Payload.Decode[steerSignal]()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSteer, err)
	}
	if validationErr := validateSteeringMessages(envelope.Steer.Messages); validationErr != nil {
		return validationErr
	}
	return s.appendSignal(signal, envelope.Steer.Messages)
}

func validateSteerSignalIDs(ids []run.SignalID) error {
	if len(ids) == 0 {
		return fmt.Errorf("%w: at least one SignalID is required", ErrInvalidSteer)
	}
	seen := make(map[run.SignalID]struct{}, len(ids))
	for index, id := range ids {
		if !id.Valid() {
			return fmt.Errorf("%w: SignalID %d is invalid", ErrInvalidSteer, index)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate SignalID %q", ErrInvalidSteer, id.String())
		}
		seen[id] = struct{}{}
	}
	return nil
}

// NewSteerSignal wraps user messages as an unaddressed Signal. The mailbox
// makes steering deduplicated, ordered, and log-visible, so a resumed
// Process sees the same guidance. Steering admitted before a Tool input wait
// remains pending until the Tool batch settles, including admission during the
// Step that enters Waiting.
func NewSteerSignal(id run.SignalID, messages ...chat.Message) (run.Signal, error) {
	if !id.Valid() {
		return run.Signal{}, ErrInvalidSteer
	}
	if err := validateSteeringMessages(messages); err != nil {
		return run.Signal{}, err
	}
	payload, err := jsonv2.Marshal(steerSignal{
		Steer: steerInput{Messages: messages},
	}, jsonv2.Deterministic(true))
	if err != nil {
		return run.Signal{}, err
	}
	value, err := run.ParsePayload(payload)
	if err != nil {
		return run.Signal{}, err
	}
	return run.Signal{ID: id, Payload: value}, nil
}

const minimumSteeringMessages = 1

func validateSteeringMessages(messages []chat.Message) error {
	if len(messages) < minimumSteeringMessages {
		return fmt.Errorf("%w: at least one message is required", ErrInvalidSteer)
	}
	for index := range messages {
		message := messages[index]
		if err := message.Validate(); err != nil {
			return fmt.Errorf("%w: message %d: %w", ErrInvalidSteer, index, err)
		}
		if message.Role != chat.RoleUser {
			return fmt.Errorf("%w: message %d must have user role", ErrInvalidSteer, index)
		}
	}
	return nil
}
