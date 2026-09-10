// Package rerank adapts dedicated rerank models to the rag Refiner contract.
// It validates model indices and resolves them against the original candidate
// order, preserving document identity and result ownership.
package rerank

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"

	corererank "github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/rag"
)

// ErrNilModel rejects a dedicated reranker without its sole dependency.
var ErrNilModel = errors.New("rag: rerank model must not be nil")

// RefinerConfig binds a dedicated rerank model to portable request defaults.
type RefinerConfig struct {
	Model corererank.Model
	// Formatter defaults to [rag.TextFormatter], which rejects media with
	// [rag.ErrUnsupportedMedia].
	Formatter rag.DocumentFormatter
	TopK      int
}

func (r RefinerConfig) Validate() error {
	if lo.IsNil(r.Model) {
		return ErrNilModel
	}
	if r.TopK < 0 {
		return fmt.Errorf("%w: top K must not be negative", rag.ErrInvalidReranking)
	}
	return nil
}

// Refiner adapts a dedicated reranking model to the Refiner lifecycle. It
// formats candidates once and resolves returned indices against the immutable
// input snapshot.
type Refiner struct {
	model     corererank.Model
	formatter rag.DocumentFormatter
	topK      int
}

var _ rag.Refiner = (*Refiner)(nil)

// NewRefiner freezes defaults while leaving query-specific limits on Refine.
func NewRefiner(config RefinerConfig) (*Refiner, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	formatter := config.Formatter
	if lo.IsNil(formatter) {
		formatter = rag.TextFormatter{}
	}
	return &Refiner{model: config.Model, formatter: formatter, topK: config.TopK}, nil
}

func (r *Refiner) Refine(ctx context.Context, query rag.Query, candidates rag.Candidates) (rag.Candidates, error) {
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
			return nil, fmt.Errorf("%w: format candidate %d: %w", rag.ErrInvalidReranking, index, err)
		}
		if strings.TrimSpace(content) == "" {
			return nil, fmt.Errorf("%w: candidate %d formatted to blank content", rag.ErrInvalidReranking, index)
		}
		documents[index] = content
	}
	request, requestErr := corererank.NewRequest(query.Text(), documents)
	if requestErr != nil {
		return nil, fmt.Errorf("%w: create model request: %w", rag.ErrInvalidReranking, requestErr)
	}
	request.Options.TopK = r.topK
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("%w: model request: %w", rag.ErrInvalidReranking, err)
	}
	response, callErr := r.model.Call(ctx, request)
	if callErr != nil {
		return nil, callErr
	}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("%w: model response: %w", rag.ErrInvalidReranking, err)
	}

	ranked := make(rag.Candidates, len(response.Results))
	for position, result := range response.Results {
		ranked[position] = candidates[result.Index].Clone()
		ranked[position].Score = rag.Score(result.Score.Float64())
	}
	return ranked, nil
}
