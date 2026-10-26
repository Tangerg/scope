package vectorstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/rag"
	ragvectorstore "github.com/Tangerg/scope/rag/vectorstore"
)

type searcherFunc func(context.Context, *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error)

func (s searcherFunc) Search(ctx context.Context, request *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	return s(ctx, request)
}

func TestRetrieverHonorsCancellationBeforeAndAfterDependencies(t *testing.T) {
	for _, phase := range []string{"before", "filter", "search"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			filterCalls, searchCalls := 0, 0
			retriever, err := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
				VectorStore: searcherFunc(func(context.Context, *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
					searchCalls++
					if phase == "search" {
						cancel()
					}
					return &vectorstore.SearchResponse{}, nil
				}),
				FilterFunc: func(context.Context, rag.Query) (filter.Predicate, error) {
					filterCalls++
					if phase == "filter" {
						cancel()
					}
					return nil, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			query, err := rag.NewQuery("question")
			if err != nil {
				t.Fatal(err)
			}
			if phase == "before" {
				cancel()
			}
			result, err := retriever.Retrieve(ctx, query)
			if result != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("Retrieve = %v, %v", result, err)
			}
			if phase == "before" && filterCalls != 0 || phase != "search" && searchCalls != 0 {
				t.Fatalf("canceled operation reached dependency: filter=%d search=%d", filterCalls, searchCalls)
			}
		})
	}
}
