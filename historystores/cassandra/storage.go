package cassandra

import (
	"fmt"
	"regexp"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Cassandra TIMEUUID timestamps advance in 100-nanosecond ticks. Reserving in
// that unit prevents distinct message positions from collapsing to one UUID.
const timeUUIDTick = 100 * time.Nanosecond

func validIdentifier(value string) bool {
	return identifierPattern.MatchString(value)
}

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
