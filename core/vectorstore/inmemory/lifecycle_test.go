package inmemory_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/inmemory"
)

func indexDocuments(t *testing.T, store *inmemory.Store, documents ...*document.Document) {
	t.Helper()
	request, err := vectorstore.NewIndexRequest(documents)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Index(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestStoreClearRemovesEveryRecord(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store,
		mustDoc(t, "1", "alpha", nil),
		mustDoc(t, "2", "beta", nil),
	)
	if store.Len() != 2 {
		t.Fatalf("Len = %d, want 2", store.Len())
	}

	store.Clear()

	if store.Len() != 0 {
		t.Fatalf("Len after Clear = %d, want 0", store.Len())
	}
	request, err := vectorstore.NewSearchRequest("alpha")
	if err != nil {
		t.Fatal(err)
	}
	results, err := search(store, t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("cleared store returned %d results", len(results))
	}
}

// TestStoreClearKeepsTheStoreUsable proves Clear resets state rather than
// invalidating the store, so a caller can reuse it for the next corpus.
func TestStoreClearKeepsTheStoreUsable(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store, mustDoc(t, "1", "alpha", nil))
	store.Clear()
	indexDocuments(t, store, mustDoc(t, "2", "beta", nil))
	if store.Len() != 1 {
		t.Fatalf("Len = %d, want 1", store.Len())
	}
}

func TestNewStoreRejectsAMissingEmbeddingModel(t *testing.T) {
	if _, err := inmemory.NewStore(inmemory.StoreConfig{}); !errors.Is(err, inmemory.ErrMissingEmbeddingModel) {
		t.Fatalf("NewStore error = %v, want ErrMissingEmbeddingModel", err)
	}
	if err := (inmemory.StoreConfig{}).Validate(); !errors.Is(err, inmemory.ErrMissingEmbeddingModel) {
		t.Fatalf("Validate error = %v, want ErrMissingEmbeddingModel", err)
	}
}

// TestNewStoreAcceptsACustomSimilarity proves the score function is a real
// policy seam: search ordering must follow the configured strategy rather than
// a hardcoded cosine.
func TestNewStoreAcceptsACustomSimilarity(t *testing.T) {
	inverted := func(left, right []float64) vectorstore.Score {
		return 1 - inmemory.CosineSimilarity(left, right)
	}
	store, err := inmemory.NewStore(inmemory.StoreConfig{
		EmbeddingModel: fakeEmbeddingModel{},
		Similarity:     inverted,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexDocuments(t, store,
		mustDoc(t, "near", "alpha", nil),
		mustDoc(t, "far", "zulu zulu zulu", nil),
	)

	request, err := vectorstore.NewSearchRequest("alpha")
	if err != nil {
		t.Fatal(err)
	}
	results, err := search(store, t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].Document.ID != "far" {
		t.Fatalf("custom similarity was ignored: %#v", results)
	}
}

func TestStoreRejectsInvalidScoresBeforeSelection(t *testing.T) {
	for name, invalid := range map[string]vectorstore.Score{
		"negative": -1, "too large": 2, "nan": vectorstore.Score(math.NaN()),
		"negative infinity": vectorstore.Score(math.Inf(-1)), "positive infinity": vectorstore.Score(math.Inf(1)),
	} {
		t.Run(name, func(t *testing.T) {
			badCoordinate := vectorFor("bad")[0]
			store, err := inmemory.NewStore(inmemory.StoreConfig{
				EmbeddingModel: fakeEmbeddingModel{},
				Similarity: func(left, right []float64) vectorstore.Score {
					if left[0] == badCoordinate || right[0] == badCoordinate {
						return invalid
					}
					return 1
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			indexDocuments(t, store, mustDoc(t, "good", "good", nil), mustDoc(t, "bad", "bad", nil))
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{
				Query: "good", Options: vectorstore.SearchOptions{TopK: 1, MinScore: 0.1},
			})
			if !errors.Is(err, vectorstore.ErrInvalidScore) || response != nil {
				t.Fatalf("search = %#v, %v; want nil, ErrInvalidScore", response, err)
			}
			store.Clear()
			if store.Len() != 0 {
				t.Fatal("store could not clear after score rejection")
			}
		})
	}
}

type failingEmbeddingModel struct{ err error }

func (f failingEmbeddingModel) Call(context.Context, *embedding.Request) (*embedding.Response, error) {
	return nil, f.err
}

// TestIndexAndSearchReportEmbeddingFailures keeps a provider outage from
// silently producing an empty corpus or an empty result page.
func TestIndexAndSearchReportEmbeddingFailures(t *testing.T) {
	boom := errors.New("boom")
	store, err := inmemory.NewStore(inmemory.StoreConfig{EmbeddingModel: failingEmbeddingModel{err: boom}})
	if err != nil {
		t.Fatal(err)
	}

	indexRequest, err := vectorstore.NewIndexRequest([]*document.Document{mustDoc(t, "1", "alpha", nil)})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Index(t.Context(), indexRequest); !errors.Is(err, boom) {
		t.Fatalf("Index error = %v, want %v", err, boom)
	}

	searchRequest, err := vectorstore.NewSearchRequest("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Search(t.Context(), searchRequest); !errors.Is(err, boom) {
		t.Fatalf("Search error = %v, want %v", err, boom)
	}
}

func TestIndexRejectsInvalidRequests(t *testing.T) {
	store := newStore(t)
	if err := store.Index(t.Context(), nil); err == nil {
		t.Fatal("Index accepted a nil request")
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{}); err == nil {
		t.Fatal("Index accepted an empty request")
	}
}

func TestSearchRejectsInvalidRequests(t *testing.T) {
	store := newStore(t)
	if _, err := store.Search(t.Context(), nil); err == nil {
		t.Fatal("Search accepted a nil request")
	}
	if _, err := store.Search(t.Context(), &vectorstore.SearchRequest{}); err == nil {
		t.Fatal("Search accepted an empty query")
	}
}

// TestDeleteWhereRequiresAFilter keeps an omitted predicate from being read as
// "delete everything".
func TestDeleteWhereRequiresAFilter(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store, mustDoc(t, "1", "alpha", nil))
	if err := store.DeleteWhere(t.Context(), nil); !errors.Is(err, vectorstore.ErrMissingFilter) {
		t.Fatalf("DeleteWhere error = %v, want ErrMissingFilter", err)
	}
	if store.Len() != 1 {
		t.Fatal("DeleteWhere removed records without a filter")
	}
}

// TestDeleteWhereHonorsContextCancellation proves the scan checks the deadline
// per record instead of running the whole corpus after the caller gave up.
func TestDeleteWhereHonorsContextCancellation(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store,
		mustDoc(t, "1", "alpha", map[string]any{"drop": true}),
		mustDoc(t, "2", "beta", map[string]any{"drop": true}),
	)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := store.DeleteWhere(ctx, filter.EQ("drop", true))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteWhere error = %v, want context.Canceled", err)
	}
}

func TestDeleteIDsHonorsContextCancellation(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store, mustDoc(t, "1", "alpha", nil))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := store.DeleteIDs(ctx, []string{"1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteIDs error = %v, want context.Canceled", err)
	}
	if store.Len() != 1 {
		t.Fatal("DeleteIDs deleted after context cancellation")
	}
}

func TestDeleteIDsIgnoresAnEmptyList(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store, mustDoc(t, "1", "alpha", nil))
	if err := store.DeleteIDs(t.Context(), nil); err != nil {
		t.Fatalf("DeleteIDs error = %v", err)
	}
	if store.Len() != 1 {
		t.Fatal("empty DeleteIDs removed records")
	}
}

// TestSearchAndDeleteReportUndecodableMetadata keeps a corrupt record loud: a
// silent skip would quietly shrink both result pages and delete sets.
func TestSearchAndDeleteReportUndecodableMetadata(t *testing.T) {
	store := newStore(t)
	corrupt := &document.Document{
		ID:       "1",
		Text:     "alpha",
		Metadata: coremetadata.Map{"broken": json.RawMessage(`{`)},
	}
	request, err := vectorstore.NewIndexRequest([]*document.Document{corrupt})
	if err != nil {
		if !errors.Is(err, vectorstore.ErrInvalidDocument) {
			t.Fatalf("NewIndexRequest error = %v", err)
		}
		return
	}
	if err = store.Index(t.Context(), request); err == nil {
		t.Fatal("Index accepted a document with undecodable metadata")
	}
}

// TestSearchReportsMalformedFilters keeps a type-confused predicate from
// degrading into an empty page that looks like "no matches".
func TestSearchReportsMalformedFilters(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store, mustDoc(t, "1", "alpha", map[string]any{"name": "alpha"}))

	request, err := vectorstore.NewSearchRequest("alpha")
	if err != nil {
		t.Fatal(err)
	}
	request.Options.Filter = filter.GT("name", 1)
	if _, err := store.Search(t.Context(), request); err == nil {
		t.Fatal("Search accepted a predicate that cannot evaluate")
	}

	if err := store.DeleteWhere(t.Context(), filter.GT("name", 1)); err == nil {
		t.Fatal("DeleteWhere accepted a predicate that cannot evaluate")
	}
}
