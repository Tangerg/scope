package vectara

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestFilterConformanceUsesOnlyCoreMetadataAndNativeIdentity(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		store, _ := indexedNativeStore(t, docs)
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, hit := range response.Results {
			ids = append(ids, hit.Document.ID)
		}
		return ids, nil
	}})
}

func TestCompleteMetadataAndNativeIdentityRoundTrip(t *testing.T) {
	for _, facts := range []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "exact": json.RawMessage(`1.00000000000000001`), "$native.key": json.RawMessage(`{"nested":[null,{},9007199254740993]}`), "id": json.RawMessage(`"business id"`)}} {
		for _, id := range []string{"one", ".", "..", "unsafe/with space' OR doc.id = 'other'", "🙂id"} {
			doc := &document.Document{ID: id, Text: "complete native text🙂", Metadata: facts}
			store, fixture := indexedNativeStore(t, []*document.Document{doc})
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: filter.IsNull("missing")}})
			if err != nil || len(response.Results) != 1 {
				t.Fatalf("response=%v error=%v", response, err)
			}
			got := response.Results[0].Document
			if got.ID != doc.ID || got.Text != doc.Text || !got.Metadata.Equal(facts) || (got.Metadata == nil) != (facts == nil) {
				t.Fatalf("native fact changed: %v", got)
			}
			if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: id, Text: "replaced", Metadata: facts}}}); err != nil {
				t.Fatal(err)
			}
			if len(fixture.records) != 1 || fixture.records[id].Parts[0].Text != "replaced" {
				t.Fatal("native replacement created a second identity")
			}
			if err = store.DeleteIDs(t.Context(), []string{id, id, "unknown"}); err != nil || len(fixture.records) != 0 {
				t.Fatalf("delete error=%v records=%v", err, fixture.records)
			}
		}
	}
}

func TestCurrentSourceCannotHideCorruptionBehindPredicate(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(record map[string]any) { record["metadata"] = map[string]any{"value": 1} },
		func(record map[string]any) {
			record["metadata"] = map[string]string{metadataField: `{"key":1,"key":2}`}
		},
		func(record map[string]any) { record["metadata"] = map[string]string{"wrong": "null"} },
		func(record map[string]any) { record["parts"] = []nativePart{{Text: "one"}, {Text: "two"}} },
		func(record map[string]any) { record["parts"] = nil },
		func(record map[string]any) { record["parts"] = []nativePart{{Text: "text", Context: "other context"}} },
		func(record map[string]any) { record["parts"] = []nativePart{{Text: "text", ImageID: "image"}} },
		func(record map[string]any) { record["id"] = "other" },
		func(record map[string]any) { record["tables"] = []any{map[string]any{"id": "table"}} },
	} {
		store, fixture := indexedNativeStore(t, []*document.Document{{ID: "one", Text: "text"}})
		fixture.sourceChange = change
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "outside"), MinScore: 1}})
		if err == nil || response != nil {
			t.Fatalf("invalid source became success: %v %v", response, err)
		}
		writes, deletes := fixture.writes, len(fixture.deletes)
		if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "two", Text: "new"}}}); err == nil || fixture.writes != writes || len(fixture.deletes) != deletes {
			t.Fatal("corrupt source allowed mutation")
		}
		if _, err = NewStore(t.Context(), StoreConfig{Endpoint: fixture.url, APIKey: "isolated", CorpusKey: "corpus", DocumentBatcher: testBatcher{}}); err == nil {
			t.Fatal("constructor accepted an invalid native source")
		}
	}
}

func TestNativeRankingValidatesEveryHitBeforeThreshold(t *testing.T) {
	for _, fault := range []string{"missing ID", "missing text", "missing score", "out of scale", "old metadata", "wrong type", "wrong corpus", "duplicate", "surplus", "query failure"} {
		t.Run(fault, func(t *testing.T) {
			store, fixture := indexedNativeStore(t, []*document.Document{{ID: "one", Text: "text"}})
			fixture.queryChange = func(items []map[string]any) []map[string]any {
				items[0]["score"] = -1.0
				switch fault {
				case "missing ID":
					items[0]["document_id"] = ""
				case "missing text":
					items[0]["text"] = ""
				case "missing score":
					delete(items[0], "score")
				case "out of scale":
					items[0]["score"] = 2.0
				case "old metadata":
					items[0]["document_metadata"] = map[string]any{"value": 1}
				case "wrong type":
					items[0]["result_type"] = "image"
				case "wrong corpus":
					items[0]["corpus_key"] = "other"
				case "duplicate":
					items = append(items, items[0])
				case "surplus":
					items = append(items, items[0], items[0])
				}
				return items
			}
			fixture.queryFailure = fault == "query failure"
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 2, MinScore: 1}})
			if err == nil || response != nil {
				t.Fatalf("invalid hit became success: %v %v", response, err)
			}
		})
	}
}

func TestNativePagesFollowOpaqueKeysAndRejectRepetition(t *testing.T) {
	store, fixture := indexedNativeStore(t, []*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}})
	fixture.pages = []string{`{"documents":[{"id":"one"}],"metadata":{"page_key":"k1"}}`, `{"documents":[],"metadata":{"page_key":"k2"}}`, `{"documents":[{"id":"two"}],"metadata":{}}`}
	fixture.listed = 0
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 2}})
	if err != nil || len(response.Results) != 2 || fixture.listed != 3 {
		t.Fatalf("response=%v pages=%d err=%v", response, fixture.listed, err)
	}
	for _, pages := range [][]string{
		{`{"documents":[],"metadata":{"page_key":"repeat"}}`, `{"documents":[],"metadata":{"page_key":"repeat"}}`},
		{`{"documents":[{"id":"one"}],"metadata":{"page_key":"next"}}`, `{"documents":[{"id":"one"}],"metadata":{}}`},
		{`{"documents":null}`}, {`{}`},
	} {
		fixture.pages, fixture.listed = pages, 0
		response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
		if err == nil || response != nil {
			t.Fatalf("invalid native page became success: %v %v", response, err)
		}
	}
}

func TestNativeRawRankAcrossBoundedIdentityGroups(t *testing.T) {
	var docs []*document.Document
	for i := range identityGroupSize + 1 {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("%03d", i), Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}})
	}
	store, fixture := indexedNativeStore(t, docs)
	fixture.scores = make(map[string]float64)
	for _, doc := range docs {
		fixture.scores[doc.ID] = 0
	}
	fixture.scores[docs[len(docs)-1].ID] = math.SmallestNonzeroFloat64
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID || !slices.Equal(fixture.groups, []int{identityGroupSize, 1}) {
		t.Fatalf("groups=%v response=%v error=%v", fixture.groups, response, err)
	}
	fixture.beforeQuery = func() {
		record := fixture.records[docs[0].ID]
		record.Metadata = map[string]string{metadataField: `{"value":"changed"}`}
		fixture.records[docs[0].ID] = record
	}
	fixture.scores[docs[0].ID] = 1
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
	if err == nil || response != nil {
		t.Fatal("changed Core membership became success")
	}
}

type rejectingBatcher struct{}

func (r rejectingBatcher) Batch(context.Context, []*document.Document) ([][]*document.Document, error) {
	return nil, errors.New("late batch failure")
}

func TestIndexPreparesCompleteRequestAndRequiresNativeAcknowledgment(t *testing.T) {
	store, fixture := indexedNativeStore(t, []*document.Document{{ID: "one", Text: "original"}})
	writes, deletes := fixture.writes, len(fixture.deletes)
	store.documentBatcher = rejectingBatcher{}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "replacement"}, {ID: "two", Text: "late"}}}); err == nil || fixture.writes != writes || len(fixture.deletes) != deletes {
		t.Fatal("preparation failure changed native state")
	}
	store.documentBatcher = testBatcher{}
	for _, fault := range []string{"wrong ID", "missing ID", "wrong status"} {
		fixture.createChange = func(record *nativeDocument) {
			if fault == "wrong ID" {
				record.ID = "other"
			}
			if fault == "missing ID" {
				record.ID = ""
			}
		}
		if fault == "wrong status" {
			fixture.createStatus = http.StatusOK
		}
		if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "replacement"}}}); err == nil {
			t.Fatalf("%s became success", fault)
		}
	}
}

func TestNativeDeletionValidatesAllIDsAndPropagatesNonMissingFailures(t *testing.T) {
	store, fixture := indexedNativeStore(t, []*document.Document{{ID: "one", Text: "text"}})
	before := len(fixture.deletes)
	if err := store.DeleteIDs(t.Context(), []string{"one", " "}); err == nil || len(fixture.deletes) != before {
		t.Fatal("invalid ID allowed a deletion prefix")
	}
	for _, status := range []int{http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusOK} {
		fixture.deleteStatus = status
		before = len(fixture.deletes)
		if err := store.DeleteIDs(t.Context(), []string{"one"}); err == nil || len(fixture.deletes) != before+1 {
			t.Fatal("native failure was suppressed or retried")
		}
	}
}

func TestNativeIdentityFilterLimitIsPreparedBeforeQuery(t *testing.T) {
	longID := strings.Repeat("x", nativeFilterMaxRunes)
	store, fixture := indexedNativeStore(t, []*document.Document{{ID: longID, Text: "text"}})
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: filter.IsNull("missing")}})
	if err == nil || response != nil || len(fixture.groups) != 0 {
		t.Fatal("oversized native selector reached querying")
	}
}

func TestNativeCorpusOwnsSupportedPolicyBeforeMutation(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(policy map[string]any) { policy["key"] = "other" },
		func(policy map[string]any) { delete(policy, "enabled") },
		func(policy map[string]any) { policy["enabled"] = false },
		func(policy map[string]any) { policy["chat_history_corpus"] = true },
		func(policy map[string]any) {
			policy["custom_dimensions"] = []any{map[string]any{"name": "boost", "querying_default": 10}}
		},
	} {
		store, fixture := indexedNativeStore(t, []*document.Document{{ID: "one", Text: "text"}})
		fixture.policyChange = change
		before := len(fixture.deletes)
		if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "replace"}}}); err == nil || len(fixture.deletes) != before {
			t.Fatal("changed native policy allowed a mutation")
		}
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
		if err == nil || response != nil {
			t.Fatal("changed native policy became a success")
		}
		if _, err = NewStore(t.Context(), StoreConfig{Endpoint: fixture.url, APIKey: "isolated", CorpusKey: "corpus", DocumentBatcher: testBatcher{}}); err == nil {
			t.Fatal("unsupported native policy allowed construction")
		}
	}
}
