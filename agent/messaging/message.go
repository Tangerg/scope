package messaging

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
)

var ErrInvalidMessage = errors.New("messaging: invalid message")

// Message is a request to deliver one input to a concrete Process address.
// Recipient and WaitID must remain bound to this payload across restoration.
type Message struct {
	Recipient run.ProcessID `json:"recipient"`
	WaitID    *run.WaitID   `json:"wait_id,omitzero"`
	Payload   run.Payload   `json:"payload"`
}

func (m Message) Valid() bool {
	return m.Recipient.Valid() && (m.WaitID == nil || m.WaitID.Valid()) && m.Payload.Valid()
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

// signalRequest derives the SignalID from the sending EffectID, so every replay
// of one Effect presents the recipient with the same identity.
func (m Message) signalRequest(effectID run.EffectID) (run.SignalRequest, error) {
	id, err := run.ParseSignalID(messageSignalPrefix + run.ComputeDigest([]byte(effectID.String())).String())
	if err != nil {
		return run.SignalRequest{}, err
	}
	var waitID run.WaitID
	if m.WaitID != nil {
		waitID = *m.WaitID
	}
	return run.NewSignalRequest(id, waitID, m.Payload.JSON())
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
