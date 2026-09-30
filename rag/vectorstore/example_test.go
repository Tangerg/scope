package vectorstore_test

import (
	"context"
	"errors"
	"fmt"

	corevs "github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/rag"
	ragvectorstore "github.com/Tangerg/scope/rag/vectorstore"
)

type filterPrinter struct{}

func (filterPrinter) Search(_ context.Context, request *corevs.SearchRequest) (*corevs.SearchResponse, error) {
	fmt.Println(request.Options.Filter)
	return &corevs.SearchResponse{}, nil
}

func ExampleRetrieverConfig_filterFunc() {
	tenantKey, err := rag.NewValueKey[string]("tenant")
	if err != nil {
		panic(err)
	}
	retriever, err := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
		VectorStore: filterPrinter{},
		FilterFunc: func(_ context.Context, query rag.Query) (filter.Predicate, error) {
			tenant, found, valueErr := query.Value(tenantKey)
			if valueErr != nil {
				return nil, valueErr
			}
			if !found {
				return nil, errors.New("tenant is required")
			}
			return filter.EQ("tenant", tenant), nil
		},
	})
	if err != nil {
		panic(err)
	}
	query, err := rag.NewQuery("release notes")
	if err != nil {
		panic(err)
	}
	query, err = query.WithValue(tenantKey, "acme")
	if err != nil {
		panic(err)
	}
	if _, err := retriever.Retrieve(context.Background(), query); err != nil {
		panic(err)
	}
	// Output: tenant == 'acme'
}
