package history

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

var (
	ErrInvalidWindow  = errors.New("history: invalid message window")
	ErrWindowTooSmall = errors.New("history: message window too small")
)

var _ Reader = WindowReader{}

// WindowReader projects reads to at most limit messages while preserving a
// merged system message followed by a suffix of complete conversation turns.
// A user message starts a turn; every following assistant and tool message
// remains in that turn until the next user message. This projection owns no
// write or clear authority. Read returns ErrWindowTooSmall rather
// than splitting the newest complete turn, and the merged system message counts
// toward the configured limit.
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
	return newMessageWindow(messages, w.limit).project()
}

type conversationTurn []chat.Message

type conversationTurns []conversationTurn

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

type messageWindow struct {
	limit          int
	systemMessages []chat.Message
	turns          conversationTurns
}

func newMessageWindow(messages []chat.Message, limit int) messageWindow {
	window := messageWindow{limit: limit}
	for _, message := range messages {
		if message.Role == chat.RoleSystem {
			window.systemMessages = append(window.systemMessages, message)
			continue
		}
		if len(window.turns) == 0 || message.Role == chat.RoleUser {
			window.turns = append(window.turns, conversationTurn{})
		}
		last := len(window.turns) - 1
		window.turns[last] = append(window.turns[last], message)
	}
	return window
}

func (m messageWindow) project() ([]chat.Message, error) {
	projected := []chat.Message{}
	if len(m.systemMessages) > 0 {
		merged, err := m.mergedSystemMessage()
		if err != nil {
			return nil, err
		}
		projected = append(projected, merged)
	}

	recent, err := m.turns.suffix(m.limit - len(projected))
	if err != nil {
		return nil, err
	}
	return append(projected, recent...), nil
}

func (m messageWindow) mergedSystemMessage() (chat.Message, error) {
	merged := chat.Message{Role: chat.RoleSystem, Metadata: metadata.Map{}}
	for i, message := range m.systemMessages {
		if i > 0 {
			merged.Parts = append(merged.Parts, chat.NewTextPart("\n\n"))
		}
		merged.Parts = append(merged.Parts, message.Parts...)
		if err := merged.Metadata.Merge(message.Metadata); err != nil {
			return chat.Message{}, fmt.Errorf("history: merge system message %d metadata: %w", i, err)
		}
	}
	if len(merged.Metadata) == 0 {
		merged.Metadata = nil
	}
	return merged, nil
}
