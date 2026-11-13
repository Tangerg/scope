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
	ErrNilResponse = errors.New("rag: chat model returned a nil response")
	// ErrNoFinalUserMessage rejects requests whose active retrieval query is
	// ambiguous.
	ErrNoFinalUserMessage = errors.New("rag: chat request must end with a user message")
)

type PreparerConfig struct {
	Retriever rag.Retriever
	// QueryTransformer resolves conversational context for retrieval only;
	// augmentation still receives the active user question. Nil preserves it.
	QueryTransformer ConversationTransformer

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
// Retriever, Augmenter and QueryTransformer are safe for concurrent use.
// Preservation keeps every user Part. Text replacements require exactly one text Part and
// preserve all other Parts and their relative positions; ambiguous rewrites
// fail with [rag.ErrInvalidAugmentation] before calling the model.
type Preparer struct {
	retriever        rag.Retriever
	augmenter        rag.Augmenter
	queryTransformer ConversationTransformer
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
func (p *PreparedRequest) replaceFinalUserText(text string) error {
	original := &p.request.Messages[len(p.request.Messages)-1]
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

func (p PreparedRequest) retrievalQuery() (rag.Query, error) {
	text, err := p.finalUserText()
	if err != nil {
		return rag.Query{}, err
	}
	query, err := rag.NewQuery(text)
	if err != nil {
		return rag.Query{}, fmt.Errorf("rag: build query from final user message: %w", err)
	}

	return query, nil
}

// Retrieval is what one prepared request retrieved and cited. It belongs to RAG
// and remains available independently of model success or stream consumption.
type Retrieval struct {
	Candidates rag.Candidates
	Citations  rag.Citations
}

// Retrieval returns an owned candidate snapshot and the immutable prompt citations.
// Complete documents are never added to model response metadata implicitly.
func (p PreparedRequest) Retrieval() Retrieval {
	return Retrieval{Candidates: p.candidates.Clone(), Citations: p.citations}
}

func NewPreparer(config PreparerConfig) (*Preparer, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Preparer{retriever: config.Retriever, augmenter: config.Augmenter, queryTransformer: config.QueryTransformer}, nil
}

// Prepare snapshots request and retrieves evidence for its final user message.
// Retrieval and augmentation finish before any model or tool execution begins.
func (p *Preparer) Prepare(ctx context.Context, request *corechat.Request) (PreparedRequest, error) {
	prepared, err := newPreparedRequest(request)
	if err != nil {
		return PreparedRequest{}, err
	}
	query, err := prepared.retrievalQuery()
	if err != nil {
		return PreparedRequest{}, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PreparedRequest{}, ctxErr
	}
	retrievalQuery, err := p.transformQuery(ctx, prepared, query)
	if err != nil {
		return PreparedRequest{}, err
	}
	candidates, err := p.retriever.Retrieve(ctx, retrievalQuery)
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
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PreparedRequest{}, ctxErr
	}
	prepared.candidates = candidates
	prepared.citations = augmentation.Citations()
	if text, replace := augmentation.ReplacementText(); replace {
		if err := prepared.replaceFinalUserText(text); err != nil {
			return PreparedRequest{}, err
		}
	}
	return prepared, nil
}

func (p *Preparer) transformQuery(ctx context.Context, prepared PreparedRequest, query rag.Query) (rag.Query, error) {
	if lo.IsNil(p.queryTransformer) {
		return query, nil
	}
	input, err := NewConversationQuery(query, prepared.request.Messages[:len(prepared.request.Messages)-1])
	if err != nil {
		return rag.Query{}, err
	}
	transformed, err := p.queryTransformer.Transform(ctx, input)
	if err != nil {
		return rag.Query{}, err
	}
	if err := ctx.Err(); err != nil {
		return rag.Query{}, err
	}
	if err := transformed.Validate(); err != nil {
		return rag.Query{}, err
	}
	return transformed, nil
}

// Call runs the prepared request through next. Retrieval remains on the prepared
// request; this call never retrieves again, including on a model failure.
func (p PreparedRequest) Call(ctx context.Context, next corechat.Model) (*corechat.Response, error) {
	if p.request == nil {
		return nil, fmt.Errorf("%w: unprepared request", corechat.ErrInvalidRequest)
	}
	if lo.IsNil(next) {
		return nil, errors.New("rag: chat model is nil")
	}
	response, err := next.Call(ctx, p.request.Clone())
	if response == nil {
		if err != nil {
			return nil, err
		}
		return nil, ErrNilResponse
	}

	return response, err
}

// Stream starts model work lazily and never retrieves again; stopping iteration
// releases the downstream stream.
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
		sequence := next.Stream(ctx, p.request.Clone())
		if sequence == nil {
			yield(nil, corechat.ErrNilStream)
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
