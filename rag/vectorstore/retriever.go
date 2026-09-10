// Package vectorstore adapts core vector search to the rag Retriever contract.
// Search policy is fixed at construction; parsed per-query filters belong to
// this adapter and travel through rag's typed query values.
package vectorstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	corevs "github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/rag"
)

var vectorStoreFilterValueKey = lo.Must(rag.NewValueKey[filter.Predicate]("vector store filter"))

// FilterValueKey returns the typed query slot for a parsed per-call
// filter. Parse textual filter DSL with [filter.Parse] before attaching it.
func FilterValueKey() rag.ValueKey[filter.Predicate] { return vectorStoreFilterValueKey }

// RetrieverConfig binds one search capability and its defaults.
// Per-query filters remain on Query.
type RetrieverConfig struct {
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
	// Optional; when [FilterValueKey] is set, the per-query filter wins.
	FilterFunc func(ctx context.Context, query rag.Query) (filter.Predicate, error)
}

func (r RetrieverConfig) validate() error {
	if lo.IsNil(r.VectorStore) {
		return errors.New("rag: vector store is required")
	}
	if err := (corevs.SearchOptions{TopK: r.TopK, MinScore: r.MinScore, Mode: r.SearchMode}).Validate(); err != nil {
		return fmt.Errorf("rag: vector-store search options: %w", err)
	}

	return nil
}

var _ rag.Retriever = (*Retriever)(nil)

// Retriever retrieves candidates from a core vector store.
type Retriever struct {
	vectorStore corevs.Searcher
	options     corevs.SearchOptions
	filterFunc  func(ctx context.Context, query rag.Query) (filter.Predicate, error)
}

// NewRetriever validates the search boundary and its defaults.
func NewRetriever(config RetrieverConfig) (*Retriever, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	return &Retriever{
		vectorStore: config.VectorStore,
		options:     corevs.SearchOptions{TopK: config.TopK, MinScore: config.MinScore, Mode: config.SearchMode},
		filterFunc:  config.FilterFunc,
	}, nil
}

// Retrieve issues the configured relevance search via the underlying vector store.
func (r *Retriever) Retrieve(ctx context.Context, query rag.Query) (rag.Candidates, error) {
	if err := query.Validate(); err != nil {
		return nil, err
	}

	expr, err := r.resolveFilter(ctx, query)
	if err != nil {
		return nil, err
	}

	request := &corevs.SearchRequest{
		Query:   query.Text(),
		Options: r.options,
	}
	request.Options.Filter = expr
	if validateErr := request.Validate(); validateErr != nil {
		return nil, fmt.Errorf("rag: build vector-store request: %w", validateErr)
	}
	response, err := r.vectorStore.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("rag: vector-store response: %w", err)
	}
	candidates := make(rag.Candidates, 0, len(response.Results))
	for _, result := range response.Results {
		candidates = append(candidates, rag.Candidate{Document: result.Document.Clone(), Score: rag.Score(result.Score.Float64())})
	}
	return candidates, nil
}

// resolveFilter picks the filter expression to use for this call,
// preferring the per-query [FilterValueKey] slot over the configured
// FilterFunc. Returns nil, nil when no filter applies.
func (r *Retriever) resolveFilter(ctx context.Context, query rag.Query) (filter.Predicate, error) {
	expression, exists, err := query.Value(vectorStoreFilterValueKey)
	if err != nil {
		return nil, fmt.Errorf("rag: read vector-store filter: %w", err)
	}
	if exists {
		return expression, nil
	}

	if r.filterFunc != nil {
		return r.filterFunc(ctx, query)
	}
	return nil, nil
}
