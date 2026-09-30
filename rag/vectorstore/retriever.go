// Package vectorstore adapts core vector search to the rag Retriever contract.
// Search policy is fixed at construction, and [RetrieverConfig.FilterFunc] is
// the only source of a retrieval's metadata filter.
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

type RetrieverConfig struct {
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

	// FilterFunc builds each retrieval's metadata filter from the complete
	// query and is the filter's only source. A per-query filter is a
	// caller-owned [rag.ValueKey] read here, so it composes with fixed policy
	// such as tenant isolation instead of replacing it. Nil applies no filter;
	// an error fails the retrieval before the store is searched.
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

type Retriever struct {
	vectorStore corevs.Searcher
	options     corevs.SearchOptions
	filterFunc  func(ctx context.Context, query rag.Query) (filter.Predicate, error)
}

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

func (r *Retriever) Retrieve(ctx context.Context, query rag.Query) (rag.Candidates, error) {
	if err := query.Validate(); err != nil {
		return nil, err
	}

	expr, err := r.buildFilter(ctx, query)
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
