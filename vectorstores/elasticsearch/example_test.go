package elasticsearch_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/elastic/elastic-transport-go/v8/elastictransport"
	elasticsearchsdk "github.com/elastic/go-elasticsearch/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/elasticsearch"
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
	client, err := elasticsearchsdk.New(elasticsearchsdk.WithAddresses(os.Getenv("SCOPE_ELASTICSEARCH_ENDPOINT")), elasticsearchsdk.WithBasicAuth(os.Getenv("SCOPE_ELASTICSEARCH_USERNAME"), os.Getenv("SCOPE_ELASTICSEARCH_PASSWORD")), elasticsearchsdk.WithTransportOptions(elastictransport.WithTransport(transport)))
	if err != nil {
		panic(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if closeErr := client.Close(cleanup); closeErr != nil {
			panic(closeErr)
		}
	}()
	// Provision once through the native host API before constructing Store.
	created, err := client.Indices.Create("documents", client.Indices.Create.WithContext(ctx), client.Indices.Create.WithBody(strings.NewReader(`{"mappings":{"dynamic":"strict","properties":{"content":{"type":"text"},"embedding":{"type":"dense_vector","dims":2,"similarity":"cosine"},"metadata_json":{"type":"keyword","index":false,"doc_values":false}}}}`)))
	if err != nil {
		panic(err)
	}
	closeErr := created.Body.Close()
	if created.IsError() || closeErr != nil {
		panic(errors.Join(errors.New("native index provisioning failed"), closeErr))
	}
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := elasticsearch.NewStore(ctx, elasticsearch.StoreConfig{Client: client, IndexName: "documents", EmbeddingModel: model, DocumentBatcher: exampleBatcher{}})
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
