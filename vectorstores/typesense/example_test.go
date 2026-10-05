package typesense_test

import (
	"context"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/typesense"
)

type exampleBatcher struct{}

func (e exampleBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, 32)), nil
}

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	key := os.Getenv("SCOPE_TYPESENSE_API_KEY")
	client, err := api.NewClient(os.Getenv("SCOPE_TYPESENSE_ENDPOINT"), api.WithHTTPClient(&http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}), api.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
		request.Header.Set("X-TYPESENSE-API-KEY", key)
		return nil
	}))
	if err != nil {
		panic(err)
	}
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := typesense.NewStore(ctx, typesense.StoreConfig{Client: client, CollectionName: "documents", EmbeddingModel: model, DocumentBatcher: exampleBatcher{}})
	if err != nil {
		panic(err)
	}
	if err = store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "example"}}}); err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "example"}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
