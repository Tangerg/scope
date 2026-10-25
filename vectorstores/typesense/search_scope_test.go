package typesense

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotExportSource(t *testing.T) {
	fixture := newProtocolFixture(t)
	invalidExport := "invalid exported document"
	fixture.exportOverride = &invalidExport
	store := fixture.store(constantModel(), 32)
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
}

func TestPaginationCountsNativeRankBeforeMinScoreFiltering(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 32)
	first, second := make([]any, MaxResultsPerPage), make([]any, 50)
	for index := range MaxResultsPerPage + len(second) {
		distance := float64(2)
		if index == 0 {
			distance = 0
		}
		hit := map[string]any{
			"document":        nativeRecord(t, &document.Document{ID: fmt.Sprintf("document-%d", index), Text: "text"}),
			"vector_distance": distance,
		}
		if index < len(first) {
			first[index] = hit
		} else {
			second[index-len(first)] = hit
		}
	}
	fixture.searchScript = []json.RawMessage{searchReply(t, 300, first), searchReply(t, 300, second)}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{
		Query: "query", Options: vectorstore.SearchOptions{TopK: 300, MinScore: 0.5},
	})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "document-0" || len(fixture.queries) != 2 {
		t.Fatalf("rank traversal with filtered results: response=%v error=%v pages=%d", response, err, len(fixture.queries))
	}
}

func TestSearchRejectsHitsBeyondTheRequestedPage(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 32)
	hits := make([]any, MaxResultsPerPage+1)
	for index := range hits {
		hits[index] = map[string]any{
			"document":        nativeRecord(t, &document.Document{ID: fmt.Sprintf("document-%d", index), Text: "text"}),
			"vector_distance": 0,
		}
	}
	fixture.searchScript = []json.RawMessage{searchReply(t, 300, hits)}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 300}})
	if err == nil || response != nil || len(fixture.queries) != 1 {
		t.Fatalf("oversized page advanced pagination: response=%v error=%v pages=%d", response, err, len(fixture.queries))
	}
}
