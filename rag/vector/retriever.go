// Package vector adapts core vector search to the rag Retriever contract.
// Search policy is fixed at construction, and [RetrieverConfig.FilterFunc] is
// the only source of a retrieval's metadata filter.
package vector

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"

	corevs "github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/rag"
)

// Source admits the requested space and search policy before encoding the query.
type Source interface {
	corevs.Querier
	CheckQuery(context.Context, corevs.SpaceID, corevs.SearchOptions) error
}

type RetrieverConfig struct {
	Encoder     *Encoder
	VectorStore Source

	// TopK caps the number of returned documents. Zero uses
	// [corevs.DefaultTopK]; negative values are invalid.
	TopK int

	// MinScore filters semantic matches below this relevance threshold. Hybrid
	// mode requires zero because fusion scores are not portable across stores.
	// Range [0.0, 1.0].
	MinScore corevs.Score

	// SearchMode selects semantic or native hybrid retrieval. The zero value is
	// semantic; a store that cannot honor hybrid returns a typed error.
	SearchMode corevs.SearchMode

	// FilterFunc builds each retrieval's metadata filter from the complete
	// query and is the filter's only source. A per-query filter is a
	// caller-owned [rag.ValueKey] read here, so it composes with fixed policy
	// such as tenant isolation instead of replacing it. Nil applies no filter;
	// an error fails the retrieval before the store is searched.
	FilterFunc func(ctx context.Context, query rag.Query) (filter.Predicate, error)
}

func (r RetrieverConfig) validate() error {
	if lo.IsNil(r.VectorStore) || r.Encoder == nil {
		return errors.New("rag/vector: query source and encoder are required")
	}
	if err := (corevs.SearchOptions{TopK: r.TopK, MinScore: r.MinScore, Mode: r.SearchMode}).Validate(); err != nil {
		return fmt.Errorf("rag: vector-store search options: %w", err)
	}

	return r.Encoder.Space().Validate()
}

var _ rag.Retriever = (*Retriever)(nil)

type Retriever struct {
	encoder     *Encoder
	vectorStore Source
	options     corevs.SearchOptions
	filterFunc  func(ctx context.Context, query rag.Query) (filter.Predicate, error)
}

func NewRetriever(config RetrieverConfig) (*Retriever, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	return &Retriever{
		vectorStore: config.VectorStore,
		encoder:     config.Encoder,
		options:     corevs.SearchOptions{TopK: config.TopK, MinScore: config.MinScore, Mode: config.SearchMode},
		filterFunc:  config.FilterFunc,
	}, nil
}

func (r *Retriever) Retrieve(ctx context.Context, query rag.Query) (rag.Candidates, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}

	expr, err := r.buildFilter(ctx, query)
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	options := r.options
	options.Filter = expr
	if validateErr := options.Validate(); validateErr != nil {
		return nil, validateErr
	}
	if checkQueryErr := r.vectorStore.CheckQuery(ctx, r.encoder.Space(), options); checkQueryErr != nil {
		return nil, checkQueryErr
	}
	request, err := r.encoder.EncodeQuery(ctx, query.Text(), options)
	if err != nil {
		return nil, err
	}
	// Validate the reply against the admitted grant even if a third-party
	// adapter rewrites its local query while building a native request.
	wire := *request
	wire.Values = slices.Clone(request.Values)
	response, err := r.vectorStore.Query(ctx, &wire)
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err := request.ValidateResponse(response); err != nil {
		return nil, fmt.Errorf("rag: vector-store response: %w", err)
	}
	candidates := make(rag.Candidates, 0, len(response.Results))
	for _, result := range response.Results {
		candidates = append(candidates, rag.Candidate{Document: result.Document.Clone(), Score: rag.Score(result.Score.Float64())})
	}
	return candidates, nil
}

func (r *Retriever) buildFilter(ctx context.Context, query rag.Query) (filter.Predicate, error) {
	if r.filterFunc == nil {
		return nil, nil
	}
	expr, err := r.filterFunc(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("rag: build vector-store filter: %w", err)
	}
	return expr, nil
}
