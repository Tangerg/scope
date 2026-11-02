package vector_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	corevs "github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/rag"
	ragvectorstore "github.com/Tangerg/scope/rag/vector"
)

type filterPrinter struct{}

func (filterPrinter) Query(_ context.Context, request *corevs.Query) (*corevs.SearchResponse, error) {
	fmt.Println(request.Options.Filter)
	return &corevs.SearchResponse{}, nil
}

func ExampleRetrieverConfig_filterFunc() {
	tenantKey, err := rag.NewValueKey[string]("tenant")
	if err != nil {
		panic(err)
	}
	retriever, err := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
		Encoder: demoEncoder(), VectorStore: filterPrinter{},
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

func (filterPrinter) CheckQuery(context.Context, corevs.SpaceID, corevs.SearchOptions) error {
	return nil
}

func demoEncoder() *ragvectorstore.Encoder {
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
	})
	binding := embeddingclient.Config{Model: model}
	encoder, err := ragvectorstore.NewEncoder(ragvectorstore.EncoderConfig{Space: "example-v1", Documents: binding, Queries: binding})
	if err != nil {
		panic(err)
	}
	return encoder
}
