package history

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

var (
	ErrInvalidWindow  = errors.New("history: invalid message window")
	ErrWindowTooSmall = errors.New("history: message window too small")
)

var _ Reader = WindowReader{}

// WindowReader projects reads to at most limit messages: a suffix of complete
// conversation turns. A user message starts a turn; every following assistant
// and tool message remains in that turn until the next user message. This
// projection owns no write or clear authority. Read returns ErrWindowTooSmall
// rather than splitting the newest complete turn.
type WindowReader struct {
	reader Reader
	limit  int
}

func NewWindowReader(reader Reader, limit int) (WindowReader, error) {
	if lo.IsNil(reader) {
		return WindowReader{}, ErrNilReader
	}
	if limit <= 0 {
		return WindowReader{}, fmt.Errorf("%w: limit must be greater than zero", ErrInvalidWindow)
	}
	return WindowReader{reader: reader, limit: limit}, nil
}

func (w WindowReader) Read(ctx context.Context, conversationID ConversationID) ([]chat.Message, error) {
	messages, err := w.reader.Read(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	return newConversationTurns(messages).suffix(w.limit)
}

type conversationTurn []chat.Message

type conversationTurns []conversationTurn

func newConversationTurns(messages []chat.Message) conversationTurns {
	var turns conversationTurns
	for _, message := range messages {
		if len(turns) == 0 || message.Role == chat.RoleUser {
			turns = append(turns, conversationTurn{})
		}
		turns[len(turns)-1] = append(turns[len(turns)-1], message)
	}
	return turns
}

func (c conversationTurns) suffix(limit int) ([]chat.Message, error) {
	if len(c) == 0 {
		return []chat.Message{}, nil
	}
	newestTurnSize := len(c[len(c)-1])
	if newestTurnSize > limit {
		return nil, fmt.Errorf(
			"%w: newest turn has %d messages, available limit is %d",
			ErrWindowTooSmall,
			newestTurnSize,
			limit,
		)
	}

	start := len(c) - 1
	messageCount := newestTurnSize
	for start > 0 {
		previousSize := len(c[start-1])
		if previousSize > limit-messageCount {
			break
		}
		start--
		messageCount += previousSize
	}

	messages := make([]chat.Message, 0, messageCount)
	for _, turn := range c[start:] {
		messages = append(messages, turn...)
	}
	return messages, nil
}
