package opensearch

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestFilteredScanRejectsAZeroCosineVectorBeforeEnumeration(t *testing.T) {
	index := newNativeIndex(t)
	store := index.store()
	query := &vectorstore.Query{Space: fixtureSpace, Values: []float64{0, 0}}
	if err := query.ValidateFor(fixtureSpace); err != nil {
		t.Fatalf("probe input must satisfy the provider-neutral contract: %v", err)
	}
	if response, err := store.Query(t.Context(), query); err == nil || response != nil {
		t.Fatalf("native path accepted a zero cosine vector: %v, %v", response, err)
	}
	query.Options.Filter = filter.IsNull("tenant")
	if response, err := store.QueryByScan(t.Context(), query, fixtureBudget()); err == nil || response != nil || index.Cleared != 0 {
		t.Fatalf("zero cosine vector reached a scan: %v, %v, cleared=%d", response, err, index.Cleared)
	}
}
