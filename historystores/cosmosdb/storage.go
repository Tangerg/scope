package cosmosdb

import (
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

func encodeMessages(messages []chat.Message) ([][]byte, error) {
	return lo.MapErr(messages, func(message chat.Message, index int) ([]byte, error) {
		raw, err := message.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", index, err)
		}
		return raw, nil
	})
}

func decodeMessage(raw []byte) (chat.Message, error) {
	var message chat.Message
	if err := message.UnmarshalJSON(raw); err != nil {
		return chat.Message{}, err
	}
	return message, nil
}
