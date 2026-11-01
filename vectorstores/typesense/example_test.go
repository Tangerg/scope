package typesense_test

import (
	"context"
	"net/http"
	"os"

	"time"

	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/typesense"
)

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
	const space vectorstore.SpaceID = "encoder-v1"
	store, err := typesense.NewStore(ctx, typesense.StoreConfig{Client: client, CollectionName: "documents", Space: space})
	if err != nil {
		panic(err)
	}
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: space, Records: []vectorstore.Record{{Document: &document.Document{ID: "one", Text: "example"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: space, Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
