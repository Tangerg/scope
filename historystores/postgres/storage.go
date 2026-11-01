package postgres

import (
	"fmt"
	"regexp"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

// PostgreSQL silently truncates longer identifiers on standard builds.
const maxIdentifierBytes = 63

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validIdentifier(value string) bool {
	return len(value) <= maxIdentifierBytes && identifierPattern.MatchString(value)
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
