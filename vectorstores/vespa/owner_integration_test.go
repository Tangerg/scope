//go:build integration

package vespa

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func liveStore(t *testing.T, model fixtureModel) *Store {
	t.Helper()
	endpoint := os.Getenv("SCOPE_VESPA_ENDPOINT")
	if endpoint == "" {
		t.Fatal("SCOPE_VESPA_ENDPOINT is required with -tags=integration")
	}
	config := StoreConfig{Endpoint: endpoint, SchemaName: "scope", Namespace: "scope_native_" + strings.ToLower(rand.Text()), RankingProfile: "scope_rank", EmbeddingModel: model, DocumentBatcher: testBatcher{}, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		records, _, err := store.readSource(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		for _, record := range records {
			if _, _, err := store.sendJSON(ctx, http.MethodDelete, store.documentPath(record.document.ID), nil); err != nil {
				t.Error(err)
			}
		}
	})
	return store
}

func liveIndex(t *testing.T, store *Store, docs []*document.Document) {
	t.Helper()
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	if err := liveVisibility(t.Context(), store, len(docs)); err != nil {
		t.Fatal(err)
	}
}

// Native puts are durable before every search replica necessarily sees them.
// Visibility synchronization belongs to the isolated fixture, not Store retries.
func liveVisibility(ctx context.Context, store *Store, count int) error {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, _, readErr := store.readSource(ctx)
		if readErr != nil {
			return readErr
		}
		if len(records) == count {
			ids := make([]string, len(records))
			for index, record := range records {
				quoted, quoteErr := quoteLiteral(store.nativeID(record.document.ID))
				if quoteErr != nil {
					return quoteErr
				}
				ids[index] = quoted
			}
			allVisible := true
			for start := 0; start < len(ids); start += queryGroupSize {
				group := ids[start:min(start+queryGroupSize, len(ids))]
				matched, queryErr := store.query(ctx, map[string]any{"yql": "select documentid from scope where scope_documentid in (" + strings.Join(group, ",") + ")", "hits": len(group), "presentation.summary": defaultSummary})
				if queryErr != nil {
					return queryErr
				}
				if len(matched) != len(group) {
					allVisible = false
					break
				}
			}
			if allVisible {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("native visibility did not reach %d documents", count)
		case <-ticker.C:
		}
	}
}

func TestLiveFilterConformance(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete=%t", deletion), func(t *testing.T) {
			store := liveStore(t, fixtureModel{})
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				records, _, err := store.readSource(ctx)
				if err != nil {
					return nil, err
				}
				for _, record := range records {
					if _, _, deleteErr := store.sendJSON(ctx, http.MethodDelete, store.documentPath(record.document.ID), nil); deleteErr != nil {
						return nil, deleteErr
					}
				}
				liveIndex(t, store, docs)
				if deletion {
					if err = store.DeleteWhere(ctx, predicate); err != nil {
						return nil, err
					}
					remaining, _, readErr := store.readSource(ctx)
					if readErr != nil {
						return nil, readErr
					}
					var deleted []string
					for _, doc := range docs {
						if !slices.ContainsFunc(remaining, func(record nativeRecord) bool { return record.document.ID == doc.ID }) {
							deleted = append(deleted, doc.ID)
						}
					}
					return deleted, nil
				}
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
		})
	}
}

func TestLiveExactMetadataAndNamespace(t *testing.T) {
	store := liveStore(t, fixtureModel{})
	facts := metadata.Map{"scope_documentid": json.RawMessage(`"business ID"`), "scope_namespace": json.RawMessage(`"business namespace"`), "$key": json.RawMessage(`{"exact":9007199254740993,"huge":1e1000,"zero":{},"line":"f\no"}`)}
	docs := []*document.Document{{ID: "null", Text: "text"}, {ID: "empty", Text: "text", Metadata: metadata.Map{}}, {ID: "quote/\"\\文", Text: "text", Metadata: facts}}
	for index := range 32 {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("page-%d", index), Text: "text"})
	}
	liveIndex(t, store, docs)
	other := liveStore(t, fixtureModel{})
	liveIndex(t, other, []*document.Document{{ID: "null", Text: "other"}})
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{TopK: len(docs)}})
	if err != nil || len(response.Results) != len(docs) {
		t.Fatalf("native result count: response=%v error=%v", response, err)
	}
	for _, expected := range docs {
		index := slices.IndexFunc(response.Results, func(hit *vectorstore.SearchResult) bool { return hit.Document.ID == expected.ID })
		if index < 0 {
			t.Fatalf("native ID %q missing", expected.ID)
		}
		got := response.Results[index].Document
		if got.Text != expected.Text || (got.Metadata == nil) != (expected.Metadata == nil) || !got.Metadata.Equal(expected.Metadata) {
			t.Fatalf("native round trip %q: %v", expected.ID, got)
		}
	}
	if err = store.DeleteWhere(t.Context(), filter.EQ("scope_documentid", "business ID")); err != nil {
		t.Fatal(err)
	}
	remaining, _, err := store.readSource(t.Context())
	if err != nil || len(remaining) != len(docs)-1 {
		t.Fatalf("native literal delete: remaining=%d error=%v", len(remaining), err)
	}
}

func TestLiveMetadataConditionAndGeneratedIdentity(t *testing.T) {
	store := liveStore(t, fixtureModel{})
	facts, err := metadata.FromValues(map[string]any{"tenant": "old"})
	if err != nil {
		t.Fatal(err)
	}
	doc := &document.Document{ID: "guard", Text: "text", Metadata: facts}
	liveIndex(t, store, []*document.Document{doc})
	records, _, err := store.readSource(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("native source: rows=%d error=%v", len(records), err)
	}
	if err = doc.Metadata.Set("tenant", "new"); err != nil {
		t.Fatal(err)
	}
	liveIndex(t, store, []*document.Document{doc})
	literal, err := quoteLiteral(records[0].metadata)
	if err != nil {
		t.Fatal(err)
	}
	condition := url.Values{"condition": {"scope.scope_metadata == " + literal}}
	_, status, err := store.sendJSON(t.Context(), http.MethodDelete, store.documentPath(doc.ID)+"?"+condition.Encode(), nil)
	if err != nil || status != http.StatusPreconditionFailed {
		t.Fatalf("native metadata guard: status=%d error=%v", status, err)
	}
	current, _, err := store.readSource(t.Context())
	if err != nil || len(current) != 1 {
		t.Fatalf("changed document lost: rows=%d error=%v", len(current), err)
	}
	if err := store.DeleteWhere(t.Context(), filter.EQ("tenant", "new")); err != nil {
		t.Fatal(err)
	}
	if remaining, _, err := store.readSource(t.Context()); err != nil || len(remaining) != 0 {
		t.Fatalf("matching native delete: rows=%d error=%v", len(remaining), err)
	}
	body := map[string]any{"fields": map[string]any{identityAttribute: "fake"}}
	if _, _, err := store.sendJSON(t.Context(), http.MethodPost, store.documentPath("forbidden"), body); err == nil {
		t.Fatal("generated native ID attribute was directly writable")
	}
}

func TestLiveRankingAcrossGroups(t *testing.T) {
	store := liveStore(t, fixtureModel{vectorFor: func(text string) []float64 {
		if text == "winner" {
			return []float64{1, 0}
		}
		if text == "query" {
			return []float64{1, 0}
		}
		return []float64{0, 1}
	}})
	docs := make([]*document.Document, 129)
	for index := range docs {
		docs[index] = &document.Document{ID: fmt.Sprintf("%03d", index), Text: "ordinary"}
	}
	docs[len(docs)-1].Text = "winner"
	liveIndex(t, store, docs)
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "128" || response.Results[0].Score != 1 {
		t.Fatalf("native group ranking: response=%v error=%v", response, err)
	}
}
