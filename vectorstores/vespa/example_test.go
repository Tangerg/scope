package vespa_test

import (
	"context"
	"os"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/vespa"
)

func ExampleNewStore() {
	ctx := context.Background()
	const space vectorstore.SpaceID = "manuals/v1"
	store, err := vespa.NewStore(ctx, vespa.StoreConfig{Endpoint: os.Getenv("SCOPE_VESPA_ENDPOINT"), SchemaName: "scope", RankingProfile: "scope_rank", Space: space, Dimensions: 2})
	if err != nil {
		panic(err)
	}
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: space, Records: []vectorstore.Record{{Document: &document.Document{ID: "guide", Text: "hello"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	_, err = store.Scan(ctx, &vectorstore.Query{Space: space, Values: []float64{1, 0}}, vectorstore.ScanBudget{MaxRecords: 1000, MaxBytes: 4 << 20, Timeout: time.Second})
	if err != nil {
		panic(err)
	}
}
