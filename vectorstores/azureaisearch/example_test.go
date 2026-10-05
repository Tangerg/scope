package azureaisearch_test

import (
	"context"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/azureaisearch"
)

type exampleBatcher struct{}

func (e exampleBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, 32)), nil
}

type exampleAuthentication struct {
	key  string
	base http.RoundTripper
}

func (e exampleAuthentication) RoundTrip(request *http.Request) (*http.Response, error) {
	authenticated := request.Clone(request.Context())
	authenticated.Header.Set("api-key", e.key)
	return e.base.RoundTrip(authenticated)
}

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     exampleAuthentication{key: os.Getenv("AZURE_SEARCH_API_KEY"), base: transport},
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := azureaisearch.NewStore(ctx, azureaisearch.StoreConfig{
		Endpoint: "https://example.search.windows.net", IndexName: "documents", HTTPClient: client,
		EmbeddingModel: model, DocumentBatcher: exampleBatcher{},
	})
	if err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "example", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"document-key"}); err != nil {
		panic(err)
	}
}
