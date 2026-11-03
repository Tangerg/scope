package chat

import (
	"context"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/rag"
)

// ConversationQuery makes the history dependency of a chat query transform
// explicit. History is a private snapshot preceding the active user turn;
// generic retrieval stages receive only the resulting rag.Query.
type ConversationQuery struct {
	query   rag.Query
	history []corechat.Message
}

func NewConversationQuery(query rag.Query, history []corechat.Message) (ConversationQuery, error) {
	input := ConversationQuery{query: query, history: cloneHistory(history)}
	if err := input.Validate(); err != nil {
		return ConversationQuery{}, err
	}
	return input, nil
}

func (c ConversationQuery) Query() rag.Query            { return c.query }
func (c ConversationQuery) History() []corechat.Message { return cloneHistory(c.history) }

func (c ConversationQuery) Validate() error {
	if err := c.query.Validate(); err != nil {
		return err
	}
	for index, message := range c.history {
		if err := message.Validate(); err != nil {
			return fmt.Errorf("rag: conversation history message %d: %w", index, err)
		}
	}
	return nil
}

func cloneHistory(history []corechat.Message) []corechat.Message {
	if history == nil {
		return nil
	}
	clone := make([]corechat.Message, len(history))
	for index := range history {
		clone[index] = history[index].Clone()
	}
	return clone
}

type ConversationTransformer interface {
	Transform(context.Context, ConversationQuery) (rag.Query, error)
}

type ConversationTransformerFunc func(context.Context, ConversationQuery) (rag.Query, error)

func (c ConversationTransformerFunc) Transform(ctx context.Context, input ConversationQuery) (rag.Query, error) {
	return c(ctx, input)
}
