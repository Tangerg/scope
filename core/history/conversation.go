package history

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidConversationID = errors.New("history: invalid conversation ID")

// ConversationID identifies one history partition. Its zero value is invalid.
// Use [NewConversationID] when converting runtime input; string constants may
// be converted directly when the value is known at compile time.
type ConversationID string

func NewConversationID(value string) (ConversationID, error) {
	conversationID := ConversationID(value)
	if err := conversationID.Validate(); err != nil {
		return "", err
	}
	return conversationID, nil
}

func (c ConversationID) String() string {
	return string(c)
}

func (c ConversationID) Validate() error {
	value := c.String()
	if value == "" {
		return fmt.Errorf("%w: empty", ErrInvalidConversationID)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: invalid UTF-8", ErrInvalidConversationID)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: leading or trailing whitespace", ErrInvalidConversationID)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: contains control character %U", ErrInvalidConversationID, character)
		}
	}
	return nil
}
