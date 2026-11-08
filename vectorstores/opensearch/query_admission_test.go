package opensearch

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestFilteredScanValidatesVectorBeforeEnumeration(t *testing.T) {
	for _, sample := range []struct {
		name   string
		values []float64
	}{
		{"wrong_dimension", []float64{1}},
		{"float32_overflow", []float64{1e300, 0}},
		{"zero_cosine", []float64{0, 0}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store()
			q := &vectorstore.Query{Space: fixtureSpace, Values: sample.values}
			if err := q.ValidateFor(fixtureSpace); err != nil {
				t.Fatalf("probe input must satisfy the provider-neutral contract: %v", err)
			}
			if response, err := store.Query(t.Context(), q); err == nil || response != nil {
				t.Fatalf("native path must reject %s: response=%v error=%v", sample.name, response, err)
			}
			q.Options.Filter = filter.IsNull("tenant")
			response, err := store.QueryByScan(t.Context(), q, fixtureBudget())
			if err == nil || response != nil || fixture.cleared != 0 {
				t.Fatalf("invalid native vector must fail before scanning: response=%v error=%v scans=%d", response, err, fixture.cleared)
			}
		})
	}
}
