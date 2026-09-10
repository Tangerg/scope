// Package chat composes retrieval with chat models through the rag domain
// contracts. It owns query transforms, expansion, model-backed ranking,
// contextual prompts, and middleware for ordinary and streaming chat calls.
// Chat history and prompt policy remain here; queries, candidates, citations,
// and generation input retain their owners in rag.
package chat

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/samber/lo"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/rag"
)

var (
	// ErrNilResponse identifies a successful model call with no response.
	ErrNilResponse = errors.New("rag: chat model returned a nil response")
	// ErrNilStreamSequence identifies a streaming implementation that did
	// not return the required iterator.
	ErrNilStreamSequence = errors.New("rag: chat streamer returned a nil sequence")
	// ErrNoFinalUserMessage rejects requests whose active retrieval query is
	// ambiguous.
	ErrNoFinalUserMessage = errors.New("rag: chat request must end with a user message")
)

const (
	retrievedCandidatesMetadataKey = "rag/retrieved_candidates"
	citationsMetadataKey           = "rag/citations"
)

var historyValueKey = lo.Must(rag.NewValueKey[[]corechat.Message]("chat history"))

// HistoryValueKey returns the typed query slot for the immutable history
// snapshot produced by [Middleware] before the active user turn.
func HistoryValueKey() rag.ValueKey[[]corechat.Message] { return historyValueKey }

// MiddlewareConfig makes retrieval and augmentation independently replaceable
// while requiring both policies explicitly.
type MiddlewareConfig struct {
	// Retriever fetches documents for the latest user message. Required.
	Retriever rag.Retriever

	// Augmenter folds retrieved documents into the outgoing user message.
	// Required; use [rag.IdentityAugmenter] for retrieval without prompt changes.
	Augmenter rag.Augmenter
}

func (m MiddlewareConfig) validate() error {
	if lo.IsNil(m.Retriever) {
		return rag.ErrNilRetriever
	}
	if lo.IsNil(m.Augmenter) {
		return rag.ErrNilAugmenter
	}
	return nil
}

// Middleware owns retrieval and augmentation policy for both chat call modes.
// It is immutable after construction and safe for concurrent use when its
// Retriever and Augmenter are safe for concurrent use. Unchanged augmentation
// preserves every user Part. Text rewrites require exactly one text Part and
// preserve all other Parts and their relative positions; ambiguous rewrites
// fail with [rag.ErrInvalidAugmentation] before calling the model.
type Middleware struct {
	retriever rag.Retriever
	augmenter rag.Augmenter
}

type preparedChatRequest struct {
	request    *corechat.Request
	candidates rag.Candidates
	citations  rag.Citations
}

func newPreparedChatRequest(request *corechat.Request) (preparedChatRequest, error) {
	if request == nil {
		return preparedChatRequest{}, fmt.Errorf("%w: nil request", corechat.ErrInvalidRequest)
	}
	clone := request.Clone()
	if err := clone.Validate(); err != nil {
		return preparedChatRequest{}, err
	}
	return preparedChatRequest{request: clone}, nil
}

func (p preparedChatRequest) finalUserText() (string, error) {
	last := len(p.request.Messages) - 1
	if last < 0 || p.request.Messages[last].Role != corechat.RoleUser {
		return "", ErrNoFinalUserMessage
	}
	return p.request.Messages[last].Text(), nil
}

// A text projection cannot locate replacements across several original Parts.
// Unchanged text preserves the complete message; a rewrite replaces one Part.
func (p *preparedChatRequest) replaceFinalUserText(text string) error {
	original := &p.request.Messages[len(p.request.Messages)-1]
	if text == original.Text() {
		return nil
	}
	textIndex := -1
	for index, part := range original.Parts {
		if part.Kind != corechat.PartText {
			continue
		}
		if textIndex >= 0 {
			return fmt.Errorf("%w: text rewrite requires exactly one text part", rag.ErrInvalidAugmentation)
		}
		textIndex = index
	}
	if textIndex < 0 {
		return fmt.Errorf("%w: text rewrite requires exactly one text part", rag.ErrInvalidAugmentation)
	}
	original.Parts[textIndex].Text = text
	return nil
}

func (p preparedChatRequest) history() []corechat.Message {
	history := make([]corechat.Message, len(p.request.Messages)-1)
	for index := range history {
		history[index] = p.request.Messages[index].Clone()
	}
	return history
}

func (p preparedChatRequest) attachRetrievalMetadata(target **corechat.ResponseMetadata) error {
	if *target == nil {
		*target = &corechat.ResponseMetadata{}
	}
	metadata := *target
	if err := metadata.Extra.Set(retrievedCandidatesMetadataKey, p.candidates); err != nil {
		return err
	}
	if len(p.citations) == 0 {
		delete(metadata.Extra, citationsMetadataKey)
		return nil
	}
	return metadata.Extra.Set(citationsMetadataKey, p.citations)
}

// CandidatesFromMetadata returns the candidates attached by [NewMiddleware].
// Streaming responses attach the payload to their first non-nil delta only.
// corechat.ResponseAccumulator retains it in the complete response.
func CandidatesFromMetadata(metadata *corechat.ResponseMetadata) (rag.Candidates, bool, error) {
	if metadata == nil {
		return nil, false, nil
	}
	candidates, found, err := metadata.Extra.Decode[rag.Candidates](retrievedCandidatesMetadataKey)
	if err != nil {
		return nil, found, fmt.Errorf("rag: decode retrieved candidates: %w", err)
	}
	if found {
		if err := candidates.Validate(); err != nil {
			return nil, true, fmt.Errorf("rag: decode retrieved candidates: %w", err)
		}
	}
	return candidates, found, nil
}

// CitationsFromMetadata returns the ordered citation mapping produced by the
// configured augmenter. The boolean reports whether citation metadata was
// present.
func CitationsFromMetadata(metadata *corechat.ResponseMetadata) (rag.Citations, bool, error) {
	if metadata == nil {
		return nil, false, nil
	}
	citations, found, err := metadata.Extra.Decode[rag.Citations](citationsMetadataKey)
	if err != nil {
		return nil, found, fmt.Errorf("rag: decode citations: %w", err)
	}
	if found {
		if err := citations.Validate(); err != nil {
			return nil, true, fmt.Errorf("rag: decode citations: %w", err)
		}
	}
	return citations, found, nil
}

// NewMiddleware freezes the two-stage RAG policy for both call modes.
func NewMiddleware(config MiddlewareConfig) (*Middleware, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Middleware{retriever: config.Retriever, augmenter: config.Augmenter}, nil
}

func (m *Middleware) prepare(ctx context.Context, request *corechat.Request) (preparedChatRequest, error) {
	prepared, err := newPreparedChatRequest(request)
	if err != nil {
		return preparedChatRequest{}, err
	}
	text, err := prepared.finalUserText()
	if err != nil {
		return preparedChatRequest{}, err
	}
	query, err := rag.NewQuery(text)
	if err != nil {
		return preparedChatRequest{}, fmt.Errorf("rag: build query from final user message: %w", err)
	}

	query, err = query.WithValue(historyValueKey, prepared.history())
	if err != nil {
		return preparedChatRequest{}, fmt.Errorf("rag: attach chat history: %w", err)
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return preparedChatRequest{}, ctxErr
	}
	candidates, err := m.retriever.Retrieve(ctx, query)
	if err != nil {
		return preparedChatRequest{}, err
	}
	if candidateErr := candidates.Validate(); candidateErr != nil {
		return preparedChatRequest{}, candidateErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return preparedChatRequest{}, ctxErr
	}
	augmentation, err := m.augmenter.Augment(ctx, query, candidates)
	if err != nil {
		return preparedChatRequest{}, err
	}
	if augmentationErr := augmentation.Validate(); augmentationErr != nil {
		return preparedChatRequest{}, augmentationErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return preparedChatRequest{}, ctxErr
	}
	prepared.candidates = candidates
	prepared.citations = augmentation.Citations()
	if err := prepared.replaceFinalUserText(augmentation.Text()); err != nil {
		return preparedChatRequest{}, err
	}
	return prepared, nil
}

func (m *Middleware) call(ctx context.Context, request *corechat.Request, next corechat.Model) (*corechat.Response, error) {
	prepared, err := m.prepare(ctx, request)
	if err != nil {
		return nil, err
	}

	response, err := next.Call(ctx, prepared.request)
	if response == nil {
		if err != nil {
			return nil, err
		}
		return nil, ErrNilResponse
	}
	if extensionErr := prepared.attachRetrievalMetadata(&response.Metadata); extensionErr != nil {
		return response, errors.Join(err, extensionErr)
	}
	return response, err
}

func (m *Middleware) stream(ctx context.Context, request *corechat.Request, next corechat.Streamer) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		prepared, err := m.prepare(ctx, request)
		if err != nil {
			yield(nil, err)
			return
		}

		sequence := next.Stream(ctx, prepared.request)
		if sequence == nil {
			yield(nil, ErrNilStreamSequence)
			return
		}
		attached := false
		for delta, streamErr := range sequence {
			if delta == nil {
				if streamErr != nil {
					yield(nil, streamErr)
					return
				}
				yield(nil, ErrNilResponse)
				return
			}
			if !attached {
				if extensionErr := prepared.attachRetrievalMetadata(&delta.Metadata); extensionErr != nil {
					yield(delta, errors.Join(streamErr, extensionErr))
					return
				}
				attached = true
			}
			if streamErr != nil {
				yield(delta, streamErr)
				return
			}
			if !yield(delta, nil) {
				return
			}
		}
	}
}

func (m *Middleware) Call(next corechat.Model) corechat.Model {
	if lo.IsNil(next) {
		return nil
	}
	return corechat.ModelFunc(func(ctx context.Context, request *corechat.Request) (*corechat.Response, error) {
		return m.call(ctx, request, next)
	})
}

func (m *Middleware) Stream(next corechat.Streamer) corechat.Streamer {
	if lo.IsNil(next) {
		return nil
	}
	return corechat.StreamerFunc(func(ctx context.Context, request *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
		return m.stream(ctx, request, next)
	})
}
