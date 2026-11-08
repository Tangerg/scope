package milvus

import (
	"testing"

	"github.com/milvus-io/milvus/client/v3/entity"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestFilteredScanValidatesVectorBeforeEnumeration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []float64
	}{
		{"wrong_dimension", []float64{1}},
		{"float32_overflow", []float64{1e300, 0}},
		{"zero_cosine", []float64{0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, service := nativeStore(t, entity.COSINE)
			request := &vectorstore.Query{Space: fixtureSpace, Values: tc.values}
			if err := request.ValidateFor(fixtureSpace); err != nil {
				t.Fatalf("Core rejected input first: %v", err)
			}
			if _, err := store.Query(t.Context(), request); err == nil {
				t.Fatal("unfiltered native query should reject")
			}
			request.Options.Filter = filter.IsNull("tenant")
			response, err := store.QueryByScan(t.Context(), request, fixtureBudget())
			if err == nil || response != nil {
				t.Fatalf("invalid native vector was accepted: %#v, %v", response, err)
			}
			service.mu.Lock()
			defer service.mu.Unlock()
			if service.queries != 0 || service.searches != 0 {
				t.Fatalf("scans=%d searches=%d", service.queries, service.searches)
			}
		})
	}
}
