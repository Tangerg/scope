package weaviate

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func indexedNativeStore(t *testing.T, docs []*document.Document) (*Store, *nativeFixture) {
	t.Helper()
	fixture := &nativeFixture{}
	store, _ := newNativeStore(t, fixture)
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	return store, fixture
}

func TestFilterSelectionUsesCompleteMetadata(t *testing.T) {
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		t.Run(string(mode), func(t *testing.T) {
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				store, fixture := indexedNativeStore(t, docs)
				response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate, Mode: mode}})
				if err != nil {
					return nil, err
				}
				var ids []string
				for _, hit := range response.Results {
					ids = append(ids, hit.Document.ID)
				}
				if err = store.DeleteIDs(ctx, ids); err != nil {
					return nil, err
				}
				for _, id := range ids {
					if _, kept := fixture.objects[id]; kept {
						return nil, fmt.Errorf("conditional deletion retained native match %s", id)
					}
				}
				return ids, nil
			}})
		})
	}
}

func TestMetadataSelectionExhaustsShortNativePages(t *testing.T) {
	docs := []*document.Document{{ID: "00000000-0000-0000-0000-000000000001", Text: "text"}, {ID: "00000000-0000-0000-0000-000000000002", Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}}}
	store, fixture := indexedNativeStore(t, docs)
	fixture.shortPages = true
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[1].ID {
		t.Fatalf("response=%v error=%v", response, err)
	}
}

func TestSemanticGroupsMergeRawDistanceAndHybridUsesOneFusion(t *testing.T) {
	var docs []*document.Document
	for i := range metadataScanPageSize + 1 {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1), Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}})
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		fixture := &nativeFixture{class: nativeClass("Documents", distanceDot)}
		store, _ := newNativeStore(t, fixture)
		if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
			t.Fatal(err)
		}
		fixture.distances = make(map[string]float64)
		for i, doc := range docs {
			fixture.distances[doc.ID] = -float64(i + 40)
		}
		fixture.hybridScores = make(map[string]float64)
		for _, doc := range docs {
			fixture.hybridScores[doc.ID] = .5
		}
		fixture.hybridScores[docs[len(docs)-1].ID] = 1
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Mode: mode, Filter: filter.EQ("value", "x")}})
		if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID {
			t.Fatalf("mode=%s response=%v error=%v", mode, response, err)
		}
		if mode == vectorstore.SearchModeSemantic && !slices.Equal(fixture.groups, []int{metadataScanPageSize, 1}) {
			t.Fatalf("groups=%v", fixture.groups)
		}
		if mode == vectorstore.SearchModeHybrid && !slices.Equal(fixture.hybrids, []int{len(docs)}) {
			t.Fatalf("hybrid was split into competing score scales: %v", fixture.hybrids)
		}
	}
}
