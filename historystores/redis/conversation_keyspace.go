package redis

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/history"
)

const (
	messagesSuffix = ":messages"
	ledgerSuffix   = ":receipts"
)

type conversationKeyspace struct{ prefix string }

func (c conversationKeyspace) tagged(id history.ConversationID) string {
	return c.prefix + "{" + base64.RawURLEncoding.EncodeToString([]byte(id)) + "}"
}

func (c conversationKeyspace) messages(id history.ConversationID) string {
	return c.tagged(id) + messagesSuffix
}
func (c conversationKeyspace) ledger(id history.ConversationID) string {
	return c.tagged(id) + ledgerSuffix
}
func (c conversationKeyspace) pattern() string {
	return scanPatternEscaper.Replace(c.prefix) + "{*}" + messagesSuffix
}

func (c conversationKeyspace) conversation(key string) (history.ConversationID, bool, error) {
	encoded, ok := strings.CutPrefix(key, c.prefix+"{")
	if !ok {
		return "", false, nil
	}
	encoded, ok = strings.CutSuffix(encoded, "}"+messagesSuffix)
	if !ok {
		return "", false, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false, fmt.Errorf("redis: malformed history key: %w", err)
	}
	id := history.ConversationID(decoded)
	if err := id.Validate(); err != nil {
		return "", false, err
	}
	if c.messages(id) != key {
		return "", false, fmt.Errorf("redis: noncanonical history key")
	}
	return id, true, nil
}
