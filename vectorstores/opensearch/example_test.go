package opensearch_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	opensearchsdk "github.com/opensearch-project/opensearch-go/v4"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/opensearch"
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
	client, err := opensearchsdk.NewClient(opensearchsdk.Config{Addresses: []string{os.Getenv("SCOPE_OPENSEARCH_ENDPOINT")}, Username: os.Getenv("SCOPE_OPENSEARCH_USERNAME"), Password: os.Getenv("SCOPE_OPENSEARCH_PASSWORD"), Transport: transport})
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			panic(closeErr)
		}
	}()
	// Provision once through the native host API before constructing Store.
	provisionRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, "/documents", strings.NewReader(`{"settings":{"index.knn":true,"index.knn.derived_source.enabled":false,"index.derived_source.enabled":false},"mappings":{"dynamic":"strict","properties":{"content":{"type":"text"},"embedding":{"type":"knn_vector","dimension":2,"method":{"name":"hnsw","engine":"lucene","space_type":"cosinesimil"}},"metadata_json":{"type":"keyword","index":false,"doc_values":false}}}}`))
	if err != nil {
		panic(err)
	}
	provisionRequest.Header.Set("Content-Type", "application/json")
	created, err := client.Stream(provisionRequest)
	if err != nil {
		panic(err)
	}
	closeErr := created.Body.Close()
	if created.StatusCode < 200 || created.StatusCode >= 300 || closeErr != nil {
		panic(errors.Join(errors.New("native index provisioning failed"), closeErr))
	}

	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := opensearch.NewStore(ctx, opensearch.StoreConfig{Client: client, IndexName: "documents", EmbeddingModel: model, DocumentBatcher: exampleBatcher{}})
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
