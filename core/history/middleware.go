package history

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

var ErrNilStream = errors.New("history: middleware: nil stream sequence")

// MiddlewareConfig binds history to one explicit partition. Calls that do not
// use history should use the undecorated model.
type MiddlewareConfig struct {
	Store          ReadWriter
	ConversationID ConversationID
}

// Middleware replays and persists history around synchronous and streaming
// chat capabilities. It is immutable and as safe for concurrent use as its
// Store, but it does not serialize Read/model/Write turns of one conversation.
// Its zero value is invalid; construct it with NewMiddleware.
type Middleware struct {
	store          ReadWriter
	conversationID ConversationID
}

func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	if lo.IsNil(config.Store) {
		return Middleware{}, ErrNilStore
	}
	if err := config.ConversationID.Validate(); err != nil {
		return Middleware{}, err
	}
	return Middleware{store: config.Store, conversationID: config.ConversationID}, nil
}

// Call is a [chat.CallMiddleware]. The response result is the canonical
// assistant message persisted to history. Bound calls reject invalid complete
// responses before applying the history retention policy.
func (m Middleware) Call(next chat.Model) chat.Model {
	return chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
		prepared, fresh, err := m.prepare(ctx, request)
		if err != nil {
			return nil, &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: err}
		}
		response, err := next.Call(ctx, prepared)
		if err != nil {
			return response, err
		}
		if err := response.Validate(); err != nil {
			return response, fmt.Errorf("history: middleware: model response: %w", err)
		}
		if err := m.persist(ctx, fresh, response.Output.Message); err != nil {
			return response, err
		}
		return response, nil
	})
}

// Stream is a [chat.StreamMiddleware]. History I/O remains lazy: no read occurs
// until the returned sequence is iterated. Fresh input and the accumulated
// assistant response are persisted only after natural, error-free completion.
func (m Middleware) Stream(next chat.Streamer) chat.Streamer {
	return chat.StreamerFunc(func(ctx context.Context, request *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			m.stream(ctx, next, request, yield)
		}
	})
}

func (m Middleware) stream(
	ctx context.Context,
	next chat.Streamer,
	request *chat.Request,
	yield func(*chat.ResponseDelta, error) bool,
) {
	prepared, fresh, err := m.prepare(ctx, request)
	if err != nil {
		yield(nil, &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: err})
		return
	}
	sequence := next.Stream(ctx, prepared)
	if sequence == nil {
		yield(nil, ErrNilStream)
		return
	}

	stream := historyStream{yield: yield, natural: true}
	sequence(stream.consume)
	if !stream.natural {
		return
	}
	response, err := stream.accumulator.Response()
	if err != nil {
		yield(nil, fmt.Errorf("history: middleware: complete stream: %w", err))
		return
	}
	if err := m.persist(ctx, fresh, response.Output.Message); err != nil {
		yield(nil, err)
	}
}

type historyStream struct {
	yield       func(*chat.ResponseDelta, error) bool
	accumulator chat.ResponseAccumulator
	natural     bool
}

func (h *historyStream) consume(chunk *chat.ResponseDelta, streamErr error) bool {
	if !h.natural {
		return false
	}
	if streamErr != nil {
		return h.stop(chunk, streamErr)
	}
	if err := h.accumulator.Add(chunk); err != nil {
		return h.stop(nil, fmt.Errorf("history: middleware: accumulate stream: %w", err))
	}
	if !h.yield(chunk, nil) {
		h.natural = false
		return false
	}
	return true
}

func (h *historyStream) stop(response *chat.ResponseDelta, err error) bool {
	h.natural = false
	h.yield(response, err)
	return false
}

func (m Middleware) prepare(
	ctx context.Context,
	request *chat.Request,
) (*chat.Request, historyMessages, error) {
	prepared, err := m.snapshotRequest(request)
	if err != nil {
		return nil, nil, fmt.Errorf("history: middleware: prepare request: %w", err)
	}
	systems, fresh, err := historyMessages(prepared.Messages).openTurn()
	if err != nil {
		return nil, nil, err
	}
	stored, err := m.store.Read(ctx, m.conversationID)
	if err != nil {
		return nil, nil, fmt.Errorf("history: middleware: read history: %w", err)
	}
	for index := range stored {
		if err := stored[index].Validate(); err != nil {
			return nil, nil, fmt.Errorf("history: middleware: stored message %d: %w", index, err)
		}
	}

	freshSnapshot := fresh.clone()
	prepared.Messages = make([]chat.Message, 0, len(systems)+len(stored)+len(fresh))
	prepared.Messages = append(prepared.Messages, systems...)
	for _, message := range stored {
		if message.Role != chat.RoleSystem {
			prepared.Messages = append(prepared.Messages, message)
		}
	}
	prepared.Messages = append(prepared.Messages, fresh...)
	if err := prepared.Validate(); err != nil {
		return nil, nil, fmt.Errorf("history: middleware: assemble request history: %w", err)
	}
	return prepared, freshSnapshot, nil
}

func (m Middleware) persist(
	ctx context.Context,
	fresh historyMessages,
	assistant *chat.Message,
) error {
	if !completesTurn(assistant) {
		return nil
	}
	// A failed or partial write is reconciled against the submitted batch,
	// including when an adapter reuses or rewrites its argument buffers.
	messages := append(fresh, assistant.Clone())
	outcome, err := m.store.Write(ctx, m.conversationID, messages.clone()...)
	if invalid := outcome.ValidateFor(len(messages), err); invalid != nil {
		err = errors.Join(err, invalid)
		// Contradictory adapter facts cannot authorize a retry of any message.
		outcome = WriteOutcome{Uncertain: len(messages) > 0}
	}
	if err != nil {
		failure := &CommitError{conversationID: m.conversationID, messages: messages, outcome: outcome, cause: err}
		kind := chat.CallFailureFailed
		if outcome.Uncertain {
			kind = chat.CallFailureUnknown
		}
		return &chat.CallFailure{Kind: kind, Cause: failure}
	}
	return nil
}

func (m Middleware) snapshotRequest(request *chat.Request) (*chat.Request, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: nil request", chat.ErrInvalidRequest)
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return request.Clone(), nil
}

// A tool-calling assistant leaves the turn open. The caller supplies its tool
// exchange until a final assistant closes the turn and the store owns it.
func completesTurn(message *chat.Message) bool {
	if message == nil || message.Role != chat.RoleAssistant {
		return false
	}
	for _, part := range message.Parts {
		if part.Kind == chat.PartToolCall {
			return false
		}
	}
	return true
}

type historyMessages []chat.Message

func (h historyMessages) clone() historyMessages {
	messages := make(historyMessages, len(h))
	for i := range h {
		messages[i] = h[i].Clone()
	}
	return messages
}

func (h historyMessages) openTurn() (systems, fresh historyMessages, err error) {
	start := slices.IndexFunc(h, func(message chat.Message) bool { return message.Role != chat.RoleSystem })
	if start < 0 || h[start].Role != chat.RoleUser {
		return nil, nil, fmt.Errorf("%w: history requires one current user-led turn after the system prefix", chat.ErrInvalidRequest)
	}
	for i := start + 1; i < len(h); i++ {
		if h[i].Role == chat.RoleUser || completesTurn(&h[i]) {
			return nil, nil, fmt.Errorf("%w: history owns prior turns; messages[%d] is outside the current open turn", chat.ErrInvalidRequest, i)
		}
	}
	return h[:start], h[start:], nil
}
