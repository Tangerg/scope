package messaging

import (
	"encoding/json"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

var ErrInvalidMessage = errors.New("messaging: invalid message")

// Message is a request to deliver one input to a concrete Process address.
// Recipient and WaitID must remain bound to this payload across restoration.
type Message struct {
	Recipient agent.ProcessID `json:"recipient"`
	WaitID    *agent.WaitID   `json:"wait_id,omitempty"`
	Payload   agent.Input     `json:"payload"`
}

func (m Message) Valid() bool {
	return m.Recipient.Valid() && (m.WaitID == nil || m.WaitID.Valid()) && m.Payload.Valid()
}

// Effect freezes the message as one external operation. Multiple recipients
// require independent Effects and independently visible settlements.
func (m Message) Effect() (agent.Effect, error) {
	if !m.Valid() {
		return agent.Effect{}, ErrInvalidMessage
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return agent.Effect{}, err
	}
	return agent.NewDispatcherEffect(payload)
}

func decodeMessage(effect agent.Effect) (Message, error) {
	if !effect.Valid() || effect.Target() != agent.EffectTargetDispatcher {
		return Message{}, ErrInvalidMessage
	}
	input, err := agent.ParseInput(effect.Payload())
	if err != nil {
		return Message{}, err
	}
	message, err := input.Decode[Message]()
	if err != nil {
		return Message{}, fmt.Errorf("%w: decode: %w", ErrInvalidMessage, err)
	}
	if !message.Valid() {
		return Message{}, ErrInvalidMessage
	}
	return message, nil
}
