package opensearch

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore/filter"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotAuditUnreturnedRecords(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.rows["one"] = storedDocument{Content: "text", MetadataJSON: "null", Embedding: []float32{1, 0}}
	fixture.rows["unrelated"] = storedDocument{Content: "text", MetadataJSON: "broken", Embedding: []float32{0, 1}}
	fixture.scores["one"], fixture.scores["unrelated"] = 1, 0.5
	store := fixture.store()
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if fixture.cleared != 0 || len(fixture.groups) != 1 || fixture.groups[0] != 0 {
		t.Fatal("unfiltered query opened a source cursor or restricted identities")
	}
}

func TestOrdinaryQueryNeverScansAndExplicitScanRequiresCompleteBudget(t *testing.T) {
	f := newProtocolFixture(t)
	store := f.store()
	docs := []*document.Document{{ID: "one", Text: "one"}, {ID: "two", Text: "two"}}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	q := &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{Filter: filter.IsNull("tenant")}}
	if response, err := store.Query(t.Context(), q); !errors.Is(err, vectorstore.ErrUnsupportedFilter) || response != nil || f.cleared != 0 || len(f.groups) != 0 {
		t.Fatalf("implicit scan: response=%v error=%v", response, err)
	}
	for _, limit := range []string{"records", "bytes"} {
		budget := fixtureBudget()
		if limit == "records" {
			budget.MaxRecords = 1
		} else {
			budget.MaxBytes = 1
		}
		if response, err := store.QueryByScan(t.Context(), q, budget); !errors.Is(err, vectorstore.ErrScanBudget) || response != nil || len(f.groups) != 0 {
			t.Fatalf("partial %s scan: %v %v", limit, response, err)
		}
	}
}
