package history

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
)

// MiddlewareConfig binds history to one explicit partition. Calls that do not
// use history should use the undecorated model.
type MiddlewareConfig struct {
	Reader         Reader
	Writer         Writer
	ConversationID ConversationID
}

// Middleware replays and persists history around synchronous and streaming
// chat capabilities. It is immutable and as safe for concurrent use as its
// Reader and Writer, but it does not serialize Read/model/Append turns of one conversation.
// Its zero value is invalid; construct it with NewMiddleware.
type Middleware struct {
	reader         Reader
	writer         Writer
	conversationID ConversationID
}

func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	if lo.IsNil(config.Reader) {
		return Middleware{}, ErrNilReader
	}
	if lo.IsNil(config.Writer) {
		return Middleware{}, ErrNilWriter
	}
	if err := config.ConversationID.Validate(); err != nil {
		return Middleware{}, err
	}
	return Middleware{reader: config.Reader, writer: config.Writer, conversationID: config.ConversationID}, nil
}

// Call is a [chat.CallMiddleware]. The response result is the canonical
// assistant message persisted to history. Bound calls reject invalid complete
// responses before applying the history retention policy.
func (m Middleware) Call(next chat.Model) chat.Model {
	return chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
		prepared, fresh, err := m.prepare(ctx, request)
		if err != nil {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
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
		yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
		return
	}
	sequence := next.Stream(ctx, prepared)
	if sequence == nil {
		yield(nil, chat.ErrNilStream)
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
	if err := request.Validate(); err != nil {
		return nil, nil, fmt.Errorf("history: middleware: prepare request: %w", err)
	}
	prepared := request.Clone()
	systems, fresh, err := historyMessages(prepared.Messages).openTurn()
	if err != nil {
		return nil, nil, err
	}
	stored, err := m.reader.Read(ctx, m.conversationID)
	if err != nil {
		return nil, nil, fmt.Errorf("history: middleware: read history: %w", err)
	}
	for index := range stored {
		if err := stored[index].Validate(); err != nil {
			return nil, nil, fmt.Errorf("history: middleware: stored message %d: %w", index, err)
		}
	}
	if err := historyMessages(stored).rejectSystemMessages(); err != nil {
		return nil, nil, fmt.Errorf("history: middleware: stored %w", err)
	}

	freshSnapshot := fresh.clone()
	prepared.Messages = make([]chat.Message, 0, len(systems)+len(stored)+len(fresh))
	prepared.Messages = append(prepared.Messages, systems...)
	prepared.Messages = append(prepared.Messages, stored...)
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
	messages := append(fresh, assistant.Clone())
	batch, err := NewBatch(m.conversationID, AppendID(rand.Text()), messages...)
	if err != nil {
		return &invocation.Failure{Kind: invocation.FailureFailed, Cause: err}
	}
	receipt, err := m.writer.Append(ctx, batch)
	confirmed := receipt.ValidateFor(batch) == nil
	if !confirmed && (err == nil || receipt != (Receipt{})) {
		err = errors.Join(err, ErrInvalidReceipt)
		receipt = Receipt{}
	}
	if err != nil {
		failure := &CommitError{batch: batch, receipt: receipt, cause: err}
		kind := invocation.FailureUnknown
		if confirmed || errors.Is(err, ErrConversationClosed) || errors.Is(err, ErrAppendConflict) {
			kind = invocation.FailureFailed
		}
		return &invocation.Failure{Kind: kind, Cause: failure}
	}
	return nil
}

// A tool-calling assistant leaves the turn open. The caller supplies its tool
// exchange until a final assistant closes the turn and the store owns it.
func completesTurn(message *chat.Message) bool {
	return message != nil && message.Role == chat.RoleAssistant && len(message.ToolCalls()) == 0
}

type historyMessages []chat.Message

func (h historyMessages) clone() historyMessages {
	messages := make(historyMessages, len(h))
	for i := range h {
		messages[i] = h[i].Clone()
	}
	return messages
}

func (h historyMessages) rejectSystemMessages() error {
	for index, message := range h {
		if message.Role == chat.RoleSystem {
			return fmt.Errorf("messages[%d]: %w", index, ErrSystemMessage)
		}
	}
	return nil
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
