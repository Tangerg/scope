package messaging

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
)

var ErrInvalidMessage = errors.New("messaging: invalid message")

// Message is a request to deliver one input to a concrete Process address.
// Recipient and WaitKey must remain bound to this payload across restoration.
type Message struct {
	Recipient run.ProcessID `json:"recipient"`
	WaitKey   *run.WaitKey  `json:"wait_key,omitzero"`
	Payload   run.Payload   `json:"payload"`
}

func (m Message) Valid() bool {
	return m.Recipient.Valid() && (m.WaitKey == nil || m.WaitKey.Valid()) && m.Payload.Valid()
}

// Effect freezes the message as one external operation. Multiple recipients
// require independent Effects and independently visible settlements.
func (m Message) Effect() (run.Effect, error) {
	if !m.Valid() {
		return run.Effect{}, ErrInvalidMessage
	}
	payload, err := jsonv2.Marshal(m)
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

// messageSignalPrefix is persisted in recipient receipts and replay identities.
const messageSignalPrefix = "signal:message:"

// signal derives the SignalID from the sending EffectID, so every replay
// of one Effect presents the recipient with the same identity.
func (m Message) signal(effectID run.EffectID) (run.Signal, error) {
	id, err := run.ParseSignalID(messageSignalPrefix + run.ComputeDigest([]byte(effectID.String())).String())
	if err != nil {
		return run.Signal{}, err
	}
	var waitKey run.WaitKey
	if m.WaitKey != nil {
		waitKey = *m.WaitKey
	}
	return run.Signal{ID: id, Wait: waitKey, Payload: m.Payload}, nil
}

func decodeMessage(effect run.Effect) (Message, error) {
	if !effect.Valid() || effect.Target() != run.EffectTargetDispatcher {
		return Message{}, ErrInvalidMessage
	}
	input, err := run.ParsePayload(effect.Payload())
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
