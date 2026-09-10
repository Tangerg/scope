package rag

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	corevs "github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var vectorStoreFilterValueKey = mustValueKey[filter.Predicate]("vector store filter")

// VectorStoreFilterValueKey returns the typed query slot for a parsed per-call
// filter. Parse textual filter DSL with [filter.Parse] before attaching it.
func VectorStoreFilterValueKey() ValueKey[filter.Predicate] { return vectorStoreFilterValueKey }

// VectorStoreRetrieverConfig binds one search capability and its defaults.
// Per-query filters remain on Query.
type VectorStoreRetrieverConfig struct {
	// VectorStore performs the actual relevance search. Required.
	VectorStore corevs.Searcher

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

	// FilterFunc dynamically builds a metadata filter from the complete query.
	// Optional; when [VectorStoreFilterValueKey] is set, the per-query filter wins.
	FilterFunc func(ctx context.Context, query Query) (filter.Predicate, error)
}

func (v VectorStoreRetrieverConfig) validate() error {
	if lo.IsNil(v.VectorStore) {
		return errors.New("rag: vector store is required")
	}
	if err := (corevs.SearchOptions{TopK: v.TopK, MinScore: v.MinScore, Mode: v.SearchMode}).Validate(); err != nil {
		return fmt.Errorf("rag: vector-store search options: %w", err)
	}

	return nil
}

var _ Retriever = (*VectorStoreRetriever)(nil)

// VectorStoreRetriever retrieves candidates from a core vector store.
type VectorStoreRetriever struct {
	vectorStore corevs.Searcher
	options     corevs.SearchOptions
	filterFunc  func(ctx context.Context, query Query) (filter.Predicate, error)
}

// NewVectorStoreRetriever validates the search boundary and its defaults.
func NewVectorStoreRetriever(config VectorStoreRetrieverConfig) (*VectorStoreRetriever, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	return &VectorStoreRetriever{
		vectorStore: config.VectorStore,
		options:     corevs.SearchOptions{TopK: config.TopK, MinScore: config.MinScore, Mode: config.SearchMode},
		filterFunc:  config.FilterFunc,
	}, nil
}

// Retrieve issues the configured relevance search via the underlying vector store.
func (v *VectorStoreRetriever) Retrieve(ctx context.Context, query Query) (Candidates, error) {
	if err := query.Validate(); err != nil {
		return nil, err
	}

	expr, err := v.resolveFilter(ctx, query)
	if err != nil {
		return nil, err
	}

	request := &corevs.SearchRequest{
		Query:   query.Text(),
		Options: v.options,
	}
	request.Options.Filter = expr
	if validateErr := request.Validate(); validateErr != nil {
		return nil, fmt.Errorf("rag: build vector-store request: %w", validateErr)
	}
	response, err := v.vectorStore.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("rag: vector-store response: %w", err)
	}
	candidates := make(Candidates, 0, len(response.Results))
	for _, result := range response.Results {
		candidates = append(candidates, Candidate{Document: result.Document.Clone(), Score: Score(result.Score.Float64())})
	}
	return candidates, nil
}

// resolveFilter picks the filter expression to use for this call,
// preferring the per-query [VectorStoreFilterValueKey] slot over the configured
// FilterFunc. Returns nil, nil when no filter applies.
func (v *VectorStoreRetriever) resolveFilter(ctx context.Context, query Query) (filter.Predicate, error) {
	expression, exists, err := query.Value(vectorStoreFilterValueKey)
	if err != nil {
		return nil, fmt.Errorf("rag: read vector-store filter: %w", err)
	}
	if exists {
		return expression, nil
	}

	if v.filterFunc != nil {
		return v.filterFunc(ctx, query)
	}
	return nil, nil
}
