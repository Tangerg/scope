package postgres

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"regexp"

	"github.com/Tangerg/scope/core/chat"
)

// PostgreSQL silently truncates longer identifiers on standard builds.
const maxIdentifierBytes = 63

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validIdentifier(value string) bool {
	return len(value) <= maxIdentifierBytes && identifierPattern.MatchString(value)
}

func decodeMessages(raw []byte) ([]chat.Message, error) {
	var messages []chat.Message
	if err := jsonv2.Unmarshal(raw, &messages); err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, errors.New("stored append has no messages")
	}
	return messages, nil
}
