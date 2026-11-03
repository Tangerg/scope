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
	if err := validateIdentifier(c.String()); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConversationID, err)
	}
	return nil
}

func validateIdentifier(value string) error {
	if value == "" {
		return errors.New("empty identity")
	}
	if !utf8.ValidString(value) {
		return errors.New("invalid UTF-8")
	}
	if strings.TrimSpace(value) != value {
		return errors.New("leading or trailing whitespace")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("contains control character %U", character)
		}
	}
	return nil
}
