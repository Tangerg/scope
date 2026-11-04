package azurecosmos_test

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/vectorstores/azurecosmos"
)

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
	store, err := azurecosmos.NewStore(ctx, azurecosmos.StoreConfig{Container: container, PartitionKey: "library", Space: "manual-v1"})
	if err != nil {
		panic(err)
	}
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: "manual-v1", Records: []vectorstore.Record{{Document: &document.Document{ID: "one", Text: "example"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: "manual-v1", Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err = store.DeleteMatching(ctx, vectorstore.ScanRequest{Budget: vectorstore.ScanBudget{MaxRecords: 100, MaxBytes: 1 << 20, Timeout: time.Second}, Filter: filter.IsNull("unused")}); err != nil {
		panic(err)
	}
}
