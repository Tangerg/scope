package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"

	corererank "github.com/Tangerg/scope/core/rerank"
)

// ErrNilRerankModel rejects a dedicated reranker without its sole dependency.
var ErrNilRerankModel = errors.New("rag: rerank model must not be nil")

// RerankerConfig binds a dedicated rerank model to portable request defaults.
type RerankerConfig struct {
	Model corererank.Model
	// Formatter defaults to text rendering and rejects media with ErrUnsupportedMedia.
	Formatter DocumentFormatter
	TopK      int
}

func (r RerankerConfig) Validate() error {
	if lo.IsNil(r.Model) {
		return ErrNilRerankModel
	}
	if r.TopK < 0 {
		return fmt.Errorf("%w: top K must not be negative", ErrInvalidReranking)
	}
	return nil
}

// Reranker adapts a dedicated reranking model to the Refiner lifecycle. It
// formats candidates once and resolves returned indices against the immutable
// input snapshot.
type Reranker struct {
	model     corererank.Model
	formatter DocumentFormatter
	topK      int
}

var _ Refiner = (*Reranker)(nil)

// NewReranker freezes defaults while leaving query-specific limits on Refine.
func NewReranker(config RerankerConfig) (*Reranker, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	formatter := config.Formatter
	if lo.IsNil(formatter) {
		formatter = textDocumentFormatter{}
	}
	return &Reranker{model: config.Model, formatter: formatter, topK: config.TopK}, nil
}

func (r *Reranker) Refine(ctx context.Context, query Query, candidates Candidates) (Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if err := candidates.Validate(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	documents := make([]string, len(candidates))
	for index, candidate := range candidates {
		content, err := r.formatter.Format(candidate.Document)
		if err != nil {
			return nil, fmt.Errorf("%w: format candidate %d: %w", ErrInvalidReranking, index, err)
		}
		if strings.TrimSpace(content) == "" {
			return nil, fmt.Errorf("%w: candidate %d formatted to blank content", ErrInvalidReranking, index)
		}
		documents[index] = content
	}
	request, requestErr := corererank.NewRequest(query.Text(), documents)
	if requestErr != nil {
		return nil, fmt.Errorf("%w: create model request: %w", ErrInvalidReranking, requestErr)
	}
	request.Options.TopK = r.topK
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("%w: model request: %w", ErrInvalidReranking, err)
	}
	response, callErr := r.model.Call(ctx, request)
	if callErr != nil {
		return nil, callErr
	}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("%w: model response: %w", ErrInvalidReranking, err)
	}

	ranked := make(Candidates, len(response.Results))
	for position, result := range response.Results {
		ranked[position] = candidates[result.Index].Clone()
		ranked[position].Score = Score(result.Score.Float64())
	}
	return ranked, nil
}
