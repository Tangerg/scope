package vespa_test

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/vectorstores/vespa"
)

type documentBatcher struct{}

func (d documentBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func ExampleNewStore() {
	ctx := context.Background()
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for index := range outputs {
			outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	// Deploy the current application schema described in package GoDoc first.
	store, err := vespa.NewStore(ctx, vespa.StoreConfig{Endpoint: os.Getenv("SCOPE_VESPA_ENDPOINT"), SchemaName: "scope", RankingProfile: "scope_rank", EmbeddingModel: model, DocumentBatcher: documentBatcher{}, HTTPClient: &http.Client{Timeout: 30 * time.Second}})
	if err != nil {
		panic(err)
	}
	if err = store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "current record"}}}); err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "current"}); err != nil {
		panic(err)
	}
	if err = store.DeleteWhere(ctx, filter.IsNull("tenant")); err != nil {
		panic(err)
	}
}
