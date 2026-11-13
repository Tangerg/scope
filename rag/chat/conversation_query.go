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
	if err := query.Validate(); err != nil {
		return ConversationQuery{}, err
	}
	for index, message := range history {
		if err := message.Validate(); err != nil {
			return ConversationQuery{}, fmt.Errorf("rag: conversation history message %d: %w", index, err)
		}
	}
	return ConversationQuery{query: query, history: cloneHistory(history)}, nil
}

func (c ConversationQuery) Query() rag.Query            { return c.query }
func (c ConversationQuery) History() []corechat.Message { return cloneHistory(c.history) }

// Validate rejects the zero ConversationQuery. NewConversationQuery admits the
// query and every history message once, and the snapshot is never written again.
func (c ConversationQuery) Validate() error { return c.query.Validate() }

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
