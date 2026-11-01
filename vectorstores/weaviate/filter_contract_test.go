package weaviate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func indexedNativeStore(t *testing.T, docs []*document.Document) (*Store, *nativeFixture) {
	t.Helper()
	fixture := &nativeFixture{}
	store := newNativeStore(t, fixture)
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	return store, fixture
}

func TestNativeSelectionDoesNotEnumerateOrCoerceArrays(t *testing.T) {
	docs := []*document.Document{{ID: "00000000-0000-0000-0000-000000000001", Text: "text", Metadata: metadata.Map{"v": json.RawMessage(`"x"`)}}, {ID: "00000000-0000-0000-0000-000000000002", Text: "text", Metadata: metadata.Map{"v": json.RawMessage(`["x"]`)}}}
	store, f := indexedNativeStore(t, docs)
	f.mu.Lock()
	f.sourceError = true
	f.mu.Unlock()
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		q := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("x"), Options: vectorstore.SearchOptions{Mode: mode, Filter: filter.EQ("v", "x")}}
		r, err := fixtureQuery(t.Context(), store, q)
		if err != nil || len(r.Results) != 1 || r.Results[0].Document.ID != docs[0].ID || f.scans.Load() != 0 {
			t.Fatalf("mode=%s got=%v err=%v scans=%d", mode, r, err, f.scans.Load())
		}
		q.Options.Filter = filter.EQ("v", 1)
		if r, err = fixtureQuery(t.Context(), store, q); r != nil || !errors.Is(err, vectorstore.ErrUnsupportedFilter) || f.scans.Load() != 0 {
			t.Fatalf("unsupported mode=%s got=%v err=%v scans=%d", mode, r, err, f.scans.Load())
		}
	}
}
func TestSemanticGroupsMergeRawDistanceAndHybridUsesOneFusion(t *testing.T) {
	var docs []*document.Document
	for i := range 257 {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1), Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}})
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		fixture := &nativeFixture{class: nativeClass("Documents", distanceDot)}
		store := newNativeStore(t, fixture)
		if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
			t.Fatal(err)
		}
		fixture.mu.Lock()
		fixture.distances = make(map[string]float64)
		for i, doc := range docs {
			fixture.distances[doc.ID] = -float64(i + 40)
		}
		fixture.hybridScores = make(map[string]float64)
		for _, doc := range docs {
			fixture.hybridScores[doc.ID] = .5
		}
		fixture.hybridScores[docs[len(docs)-1].ID] = 1
		fixture.mu.Unlock()
		response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: 1, Mode: mode, Filter: filter.EQ("value", "x")}})
		if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID {
			t.Fatalf("mode=%s response=%v error=%v", mode, response, err)
		}
		fixture.mu.Lock()
		groups, hybrids := slices.Clone(fixture.groups), slices.Clone(fixture.hybrids)
		fixture.mu.Unlock()
		if mode == vectorstore.SearchModeSemantic && !slices.Equal(groups, []int{1}) {
			t.Fatalf("groups=%v", groups)
		}
		if mode == vectorstore.SearchModeHybrid && !slices.Equal(hybrids, []int{1}) {
			t.Fatalf("hybrid was split into competing score scales: %v", hybrids)
		}
	}
}
