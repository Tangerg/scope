package inmemory_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"

	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/inmemory"
)

func indexDocuments(t *testing.T, store *inmemory.Store, documents ...*document.Document) {
	t.Helper()
	request := batch(documents)
	if err := store.Upsert(t.Context(), request); err != nil {
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
	request := queryFor("alpha")
	results, err := search(store, t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("cleared store returned %d results", len(results))
	}
}

func TestStoreClearKeepsTheStoreUsable(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store, mustDoc(t, "1", "alpha", nil))
	store.Clear()
	indexDocuments(t, store, mustDoc(t, "2", "beta", nil))
	if store.Len() != 1 {
		t.Fatalf("Len = %d, want 1", store.Len())
	}
}

func TestNewStoreRejectsAMissingSpace(t *testing.T) {
	if _, err := inmemory.NewStore(t.Context(), inmemory.StoreConfig{}); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatalf("NewStore error = %v, want vectorstore.ErrIncompatibleSpace", err)
	}
	if err := (inmemory.StoreConfig{}).Validate(); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatalf("Validate error = %v, want vectorstore.ErrIncompatibleSpace", err)
	}
}

func TestNewStoreAcceptsACustomSimilarity(t *testing.T) {
	inverted := func(left, right []float64) vectorstore.Score {
		return 1 - inmemory.CosineSimilarity(left, right)
	}
	store, err := inmemory.NewStore(t.Context(), inmemory.StoreConfig{
		Space: testSpace, Dimensions: 4,
		Similarity: inverted,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexDocuments(t, store,
		mustDoc(t, "near", "alpha", nil),
		mustDoc(t, "far", "zulu zulu zulu", nil),
	)

	request := queryFor("alpha")
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
			store, err := inmemory.NewStore(t.Context(), inmemory.StoreConfig{
				Space: testSpace, Dimensions: 4,
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
			response, err := store.Query(t.Context(), &vectorstore.Query{
				Space: testSpace, Values: vectorFor("good"), Options: vectorstore.SearchOptions{TopK: 1, MinScore: 0.1},
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

func TestIndexRejectsInvalidRequests(t *testing.T) {
	store := newStore(t)
	if err := store.Upsert(t.Context(), nil); err == nil {
		t.Fatal("Index accepted a nil request")
	}
	if err := store.Upsert(t.Context(), &vectorstore.Batch{}); err == nil {
		t.Fatal("Index accepted an empty request")
	}
}

func TestSearchRejectsInvalidRequests(t *testing.T) {
	store := newStore(t)
	if _, err := store.Query(t.Context(), nil); err == nil {
		t.Fatal("Search accepted a nil request")
	}
	if _, err := store.Query(t.Context(), &vectorstore.Query{}); err == nil {
		t.Fatal("Search accepted an empty query")
	}
}

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

// A record whose metadata cannot be compared fails the whole request, which
// must then delete nothing, whichever record map iteration evaluates first.
func TestDeleteWhereEvaluationFailureDeletesNothing(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store,
		mustDoc(t, "1", "alpha", map[string]any{"year": 2020}),
		mustDoc(t, "2", "beta", map[string]any{"year": "2021"}),
		mustDoc(t, "3", "gamma", map[string]any{"year": 2019}),
		mustDoc(t, "4", "delta", map[string]any{"year": 2018}),
	)
	if err := store.DeleteWhere(t.Context(), filter.LT("year", 2025)); err == nil {
		t.Fatal("DeleteWhere accepted a string compared as a number")
	}
	if store.Len() != 4 {
		t.Fatalf("failed DeleteWhere left %d of 4 records", store.Len())
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

func TestSearchAndDeleteReportUndecodableMetadata(t *testing.T) {
	store := newStore(t)
	corrupt := &document.Document{
		ID:       "1",
		Text:     "alpha",
		Metadata: coremetadata.Map{"broken": json.RawMessage(`{`)},
	}
	if err := store.Upsert(t.Context(), batch([]*document.Document{corrupt})); err == nil {
		t.Fatal("accepted corrupt document metadata")
	}
}

func TestSearchReportsMalformedFilters(t *testing.T) {
	store := newStore(t)
	indexDocuments(t, store, mustDoc(t, "1", "alpha", map[string]any{"name": "alpha"}))

	request := queryFor("alpha")
	request.Options.Filter = filter.GT("name", 1)
	if _, err := store.Query(t.Context(), request); err == nil {
		t.Fatal("Search accepted a predicate that cannot evaluate")
	}

	if err := store.DeleteWhere(t.Context(), filter.GT("name", 1)); err == nil {
		t.Fatal("DeleteWhere accepted a predicate that cannot evaluate")
	}
}
