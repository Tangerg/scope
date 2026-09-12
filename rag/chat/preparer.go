// Package chat composes retrieval with chat models through the rag domain
// contracts. It owns query transforms, expansion, model-backed ranking,
// contextual prompts, and retrieval preparation for chat requests.
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

var historyValueKey = lo.Must(rag.NewValueKey[[]corechat.Message]("chat history"))

// HistoryValueKey returns the typed query slot for the immutable history
// snapshot produced by [Preparer] before the active user turn.
func HistoryValueKey() rag.ValueKey[[]corechat.Message] { return historyValueKey }

// PreparerConfig makes retrieval and augmentation independently replaceable
// while requiring both policies explicitly.
type PreparerConfig struct {
	// Retriever fetches documents for the latest user message. Required.
	Retriever rag.Retriever

	// Augmenter folds retrieved documents into the outgoing user message.
	// Required; use [rag.IdentityAugmenter] for retrieval without prompt changes.
	Augmenter rag.Augmenter
}

func (p PreparerConfig) validate() error {
	if lo.IsNil(p.Retriever) {
		return rag.ErrNilRetriever
	}
	if lo.IsNil(p.Augmenter) {
		return rag.ErrNilAugmenter
	}
	return nil
}

// Preparer owns retrieval and augmentation at the start of a user turn.
// It is immutable after construction and safe for concurrent use when its
// Retriever and Augmenter are safe for concurrent use. Unchanged augmentation
// preserves every user Part. Text rewrites require exactly one text Part and
// preserve all other Parts and their relative positions; ambiguous rewrites
// fail with [rag.ErrInvalidAugmentation] before calling the model.
type Preparer struct {
	retriever rag.Retriever
	augmenter rag.Augmenter
}

// PreparedRequest owns an immutable augmented request and its retrieval evidence.
// Its zero value is invalid. Prepare once, then pass the complete model or tool
// orchestration to Call or Stream; downstream continuations never retrieve again.
type PreparedRequest struct {
	request    *corechat.Request
	candidates rag.Candidates
	citations  rag.Citations
}

func newPreparedRequest(request *corechat.Request) (PreparedRequest, error) {
	if request == nil {
		return PreparedRequest{}, fmt.Errorf("%w: nil request", corechat.ErrInvalidRequest)
	}
	clone := request.Clone()
	if err := clone.Validate(); err != nil {
		return PreparedRequest{}, err
	}
	return PreparedRequest{request: clone}, nil
}

func (p PreparedRequest) finalUserText() (string, error) {
	last := len(p.request.Messages) - 1
	if last < 0 || p.request.Messages[last].Role != corechat.RoleUser {
		return "", ErrNoFinalUserMessage
	}
	return p.request.Messages[last].Text(), nil
}

// A text projection cannot locate replacements across several original Parts.
// Unchanged text preserves the complete message; a rewrite replaces one Part.
func (p *PreparedRequest) replaceFinalUserText(text string) error {
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

func (p PreparedRequest) history() []corechat.Message {
	history := make([]corechat.Message, len(p.request.Messages)-1)
	for index := range history {
		history[index] = p.request.Messages[index].Clone()
	}
	return history
}

// Evidence is the retrieval result for one prepared request. It belongs to RAG
// and remains available independently of model success or stream consumption.
type Evidence struct {
	Candidates rag.Candidates
	Citations  rag.Citations
}

// Evidence returns independently owned candidates and their ordered citations.
// Complete documents are never added to model response metadata implicitly.
func (p PreparedRequest) Evidence() Evidence {
	return Evidence{Candidates: p.candidates.Clone(), Citations: p.citations.Clone()}
}

// NewPreparer freezes the retrieval and augmentation policies.
func NewPreparer(config PreparerConfig) (*Preparer, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Preparer{retriever: config.Retriever, augmenter: config.Augmenter}, nil
}

// Prepare snapshots request and retrieves evidence for its final user message.
// Retrieval and augmentation finish before any model or tool execution begins.
func (p *Preparer) Prepare(ctx context.Context, request *corechat.Request) (PreparedRequest, error) {
	prepared, err := newPreparedRequest(request)
	if err != nil {
		return PreparedRequest{}, err
	}
	text, err := prepared.finalUserText()
	if err != nil {
		return PreparedRequest{}, err
	}
	query, err := rag.NewQuery(text)
	if err != nil {
		return PreparedRequest{}, fmt.Errorf("rag: build query from final user message: %w", err)
	}

	query, err = query.WithValue(historyValueKey, prepared.history())
	if err != nil {
		return PreparedRequest{}, fmt.Errorf("rag: attach chat history: %w", err)
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return PreparedRequest{}, ctxErr
	}
	candidates, err := p.retriever.Retrieve(ctx, query)
	if err != nil {
		return PreparedRequest{}, err
	}
	if candidateErr := candidates.Validate(); candidateErr != nil {
		return PreparedRequest{}, candidateErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PreparedRequest{}, ctxErr
	}
	augmentation, err := p.augmenter.Augment(ctx, query, candidates)
	if err != nil {
		return PreparedRequest{}, err
	}
	if augmentationErr := augmentation.Validate(); augmentationErr != nil {
		return PreparedRequest{}, augmentationErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PreparedRequest{}, ctxErr
	}
	prepared.candidates = candidates
	prepared.citations = augmentation.Citations()
	if err := prepared.replaceFinalUserText(augmentation.Text()); err != nil {
		return PreparedRequest{}, err
	}
	return prepared, nil
}

// Call runs the prepared request through next. Evidence remains on the prepared
// request; this call never retrieves again, including on a model failure.
func (p PreparedRequest) Call(ctx context.Context, next corechat.Model) (*corechat.Response, error) {
	if p.request == nil {
		return nil, fmt.Errorf("%w: unprepared request", corechat.ErrInvalidRequest)
	}
	if lo.IsNil(next) {
		return nil, errors.New("rag: chat model is nil")
	}
	response, err := next.Call(ctx, p.request)
	if response == nil {
		if err != nil {
			return nil, err
		}
		return nil, ErrNilResponse
	}

	return response, err
}

// Stream starts model work lazily. Evidence remains on the prepared request.
// Preparation has already completed; stopping iteration synchronously
// releases the downstream stream through its normal iterator contract.
func (p PreparedRequest) Stream(ctx context.Context, next corechat.Streamer) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		if p.request == nil {
			yield(nil, fmt.Errorf("%w: unprepared request", corechat.ErrInvalidRequest))
			return
		}
		if lo.IsNil(next) {
			yield(nil, errors.New("rag: chat streamer is nil"))
			return
		}
		sequence := next.Stream(ctx, p.request)
		if sequence == nil {
			yield(nil, ErrNilStreamSequence)
			return
		}
		for delta, streamErr := range sequence {
			if delta == nil {
				if streamErr != nil {
					yield(nil, streamErr)
					return
				}
				yield(nil, ErrNilResponse)
				return
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
