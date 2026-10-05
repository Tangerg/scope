package azurecosmos_test

import (
	"context"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/vectorstores/azurecosmos"
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
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	credential, err := azcosmos.NewKeyCredential(os.Getenv("SCOPE_COSMOS_KEY"))
	if err != nil {
		panic(err)
	}
	client, err := azcosmos.NewClientWithKey(os.Getenv("SCOPE_COSMOS_ENDPOINT"), credential, &azcosmos.ClientOptions{ClientOptions: azcore.ClientOptions{Transport: httpClient}})
	if err != nil {
		panic(err)
	}
	container, err := client.NewContainer(os.Getenv("SCOPE_COSMOS_DATABASE"), "documents")
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
	store, err := azurecosmos.NewStore(ctx, azurecosmos.StoreConfig{Container: container, PartitionKey: "library", EmbeddingModel: model, DocumentBatcher: exampleBatcher{}})
	if err != nil {
		panic(err)
	}
	if err = store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "example"}}}); err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "example"}); err != nil {
		panic(err)
	}
	if err = store.DeleteWhere(ctx, filter.IsNull("unused")); err != nil {
		panic(err)
	}
}
