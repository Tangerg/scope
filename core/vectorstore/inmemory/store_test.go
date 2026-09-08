package inmemory_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/inmemory"
)

// fakeEmbeddingModel hashes each input text into a deterministic
// 4-dim vector. Texts sharing a common prefix have higher cosine
// similarity — gives the unit tests something realistic to assert on.
type fakeEmbeddingModel struct{}

func (fakeEmbeddingModel) Call(_ context.Context, req *embedding.Request) (*embedding.Response, error) {
	results := make([]*embedding.Output, 0, len(req.Texts))
	for _, text := range req.Texts {
		r, err := embedding.NewOutput(vectorFor(text), nil)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return embedding.NewResponse(results, &embedding.ResponseMetadata{Model: "fake"})
}

// vectorFor maps a text to a deterministic 4-dim float vector.
// Designed so that texts sharing a common prefix have higher cosine
// similarity — gives the unit tests something realistic to assert on.
func vectorFor(text string) []float64 {
	v := []float64{0, 0, 0, 0}
	for i, r := range text {
		v[i%4] += float64(r)
	}
	return v
}

func newStore(t *testing.T) *inmemory.Store {
	t.Helper()
	store, err := inmemory.NewStore(inmemory.StoreConfig{EmbeddingModel: fakeEmbeddingModel{}})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func search(
	store *inmemory.Store,
	ctx context.Context,
	request *vectorstore.SearchRequest,
) ([]*vectorstore.SearchResult, error) {
	response, err := store.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	return response.Results, nil
}

func mustDoc(t *testing.T, id, text string, metadata map[string]any) *document.Document {
	t.Helper()
	encoded, err := coremetadata.FromValues(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return &document.Document{ID: id, Text: text, Metadata: encoded}
}

func TestStore_IndexAndSearchBasics(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()

	docs := []*document.Document{
		mustDoc(t, "1", "the quick brown fox", map[string]any{"animal": "fox"}),
		mustDoc(t, "2", "the quick brown bat", map[string]any{"animal": "bat"}),
		mustDoc(t, "3", "unrelated text about ships", map[string]any{"animal": "none"}),
	}
	createReq := docs
	err := store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if got := store.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3", got)
	}

	retrieveReq := &vectorstore.SearchRequest{Query: "the quick brown fox", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	retrieveReq.Options.TopK = 2
	got, err := search(store, ctx, retrieveReq)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	// Identical vector means doc "1" must rank first.
	if got[0].Document.ID != "1" {
		t.Fatalf("top result = %q, want %q", got[0].Document.ID, "1")
	}
}

func TestStore_IndexRejectsEmptyID(t *testing.T) {
	store := newStore(t)
	docs := []*document.Document{{ID: "", Text: "x"}}
	req := docs
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: req}); err == nil {
		t.Fatal("Index should reject empty ID")
	}
}

func TestSearchRequestJSONPreservesMetadataFiltering(t *testing.T) {
	store := newStore(t)
	docs := []*document.Document{
		mustDoc(t, "alpha", "shared text", map[string]any{"tenant": "alpha"}),
		mustDoc(t, "beta", "shared text", map[string]any{"tenant": "beta"}),
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	request := &vectorstore.SearchRequest{
		Query: "shared text", Options: vectorstore.SearchOptions{Filter: filter.EQ("tenant", "alpha")},
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var restored vectorstore.SearchRequest
	if decodeErr := json.Unmarshal(data, &restored); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	response, err := store.Search(t.Context(), &restored)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "alpha" {
		t.Fatalf("restored search results = %#v, want only alpha", response.Documents())
	}
}

func TestStore_SearchHonoursTopK(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()
	docs := make([]*document.Document, 0, 10)
	for i := range 10 {
		docs = append(docs, mustDoc(t, fmt.Sprintf("d%d", i), fmt.Sprintf("text %d", i), nil))
	}
	createReq := docs
	if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	retrieveReq := &vectorstore.SearchRequest{Query: "text 3", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	retrieveReq.Options.TopK = 3
	got, err := search(store, ctx, retrieveReq)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
}

func TestStore_SearchAppliesFilter(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()
	docs := []*document.Document{
		mustDoc(t, "1", "alpha", map[string]any{"category": "a", "year": 2020}),
		mustDoc(t, "2", "alpha bravo", map[string]any{"category": "a", "year": 2024}),
		mustDoc(t, "3", "bravo", map[string]any{"category": "b", "year": 2024}),
	}
	createReq := docs
	if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	expr, err := filter.Parse(`category == 'a' AND year >= 2024`)
	if err != nil {
		t.Fatalf("filter.Parse: %v", err)
	}
	retrieveReq := &vectorstore.SearchRequest{Query: "alpha bravo", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	retrieveReq.Options.Filter = expr
	retrieveReq.Options.TopK = 5
	got, err := search(store, ctx, retrieveReq)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Document.ID != "2" {
		ids := make([]string, 0, len(got))
		for _, d := range got {
			ids = append(ids, d.Document.ID)
		}
		t.Fatalf("got ids=%v, want [\"2\"]", ids)
	}
}

func TestStore_SearchLikePattern(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()
	docs := []*document.Document{
		mustDoc(t, "1", "alpha", map[string]any{"name": "alpha-1"}),
		mustDoc(t, "2", "beta", map[string]any{"name": "beta-2"}),
		mustDoc(t, "3", "alpha-bravo", map[string]any{"name": "alpha-3"}),
	}
	createReq := docs
	_ = store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq})

	expr, err := filter.Parse(`name LIKE 'alpha%'`)
	if err != nil {
		t.Fatalf("filter.Parse: %v", err)
	}
	retrieveReq := &vectorstore.SearchRequest{Query: "alpha", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	retrieveReq.Options.Filter = expr
	retrieveReq.Options.TopK = 10
	got, err := search(store, ctx, retrieveReq)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	for _, match := range got {
		name, _, _ := match.Document.Metadata.Decode[string]("name")
		if !strings.HasPrefix(name, "alpha") {
			t.Errorf("doc %q has name=%q, want alpha-prefix", match.Document.ID, name)
		}
	}
}

func TestStoreLikeMatchesWildcardCharactersInMetadata(t *testing.T) {
	cases := map[string]struct {
		value   string
		pattern string
		want    bool
	}{
		"leading percent": {value: "%pending", pattern: "%", want: true},
		"suffix percent":  {value: "100% complete", pattern: "100%", want: true},
		"backtracking":    {value: "a%aa", pattern: "a%a", want: true},
		"non-match":       {value: "a%b", pattern: "a%c", want: false},
		"underscore":      {value: "50_ ready", pattern: "50_%", want: true},
		"unicode":         {value: "caf\u00e9% complete", pattern: "caf\u00e9%", want: true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			doc := mustDoc(t, "progress", "progress", map[string]any{"name": testCase.value})
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{doc}}); err != nil {
				t.Fatal(err)
			}
			expr := filter.Like("name", testCase.pattern)
			got, err := search(store, t.Context(), &vectorstore.SearchRequest{
				Query: "progress", Options: vectorstore.SearchOptions{Filter: expr},
			})
			if err != nil {
				t.Fatal(err)
			}
			wantMatches := 0
			if testCase.want {
				wantMatches = 1
			}
			if len(got) != wantMatches {
				t.Errorf("search matched %d documents, want %d", len(got), wantMatches)
			}
			if err := store.DeleteWhere(t.Context(), expr); err != nil {
				t.Fatal(err)
			}
			if store.Len() != 1-wantMatches {
				t.Errorf("delete left %d documents, want %d", store.Len(), 1-wantMatches)
			}
		})
	}
}

func TestStore_Delete(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()
	docs := []*document.Document{
		mustDoc(t, "1", "x", map[string]any{"keep": true}),
		mustDoc(t, "2", "y", map[string]any{"keep": false}),
		mustDoc(t, "3", "z", map[string]any{"keep": false}),
	}
	createReq := docs
	_ = store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq})

	expr, err := filter.Parse(`keep == false`)
	if err != nil {
		t.Fatalf("filter.Parse: %v", err)
	}
	delReq := expr
	if err := store.DeleteWhere(ctx, delReq); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := store.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
}

func TestStore_SearchMinScoreFilters(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()
	docs := []*document.Document{
		mustDoc(t, "1", "alpha", nil),
		mustDoc(t, "2", "totally unrelated zzzz", nil),
	}
	createReq := docs
	_ = store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq})

	// Get baseline scores via low threshold to find a discriminating
	// cutoff that mirrors how real callers tune MinScore.
	baseline := &vectorstore.SearchRequest{Query: "alpha", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	baseline.Options.TopK = 10
	baseline.Options.MinScore = 0.0
	all, err := search(store, ctx, baseline)
	if err != nil {
		t.Fatalf("Search baseline: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("baseline got %d, want 2", len(all))
	}

	// Pick a threshold above the unrelated doc's score but below the
	// exact match's. The fake embedder makes the exact match's score
	// strictly higher than the unrelated doc's.
	allScores := make([]vectorstore.Score, 0, len(all))
	for _, match := range all {
		allScores = append(allScores, inmemory.CosineSimilarity(
			vectorFor("alpha"), vectorFor(match.Document.Text)))
	}
	if allScores[0] <= allScores[1] {
		t.Fatalf("expected exact match to outscore unrelated, got %v", allScores)
	}
	threshold := (allScores[0] + allScores[1]) / 2

	tight := &vectorstore.SearchRequest{Query: "alpha", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	tight.Options.MinScore = threshold
	tight.Options.TopK = 10
	got, err := search(store, ctx, tight)
	if err != nil {
		t.Fatalf("Search tight: %v", err)
	}
	if len(got) != 1 || got[0].Document.ID != "1" {
		ids := make([]string, 0, len(got))
		for _, d := range got {
			ids = append(ids, d.Document.ID)
		}
		t.Fatalf("got ids=%v, want only doc 1", ids)
	}
}

func TestStore_RejectsMissingEmbeddingModel(t *testing.T) {
	if _, err := inmemory.NewStore(inmemory.StoreConfig{}); !errors.Is(err, inmemory.ErrMissingEmbeddingModel) {
		t.Fatalf("NewStore error = %v, want ErrMissingEmbeddingModel", err)
	}

	var model *nilEmbeddingModel
	if _, err := inmemory.NewStore(inmemory.StoreConfig{EmbeddingModel: model}); !errors.Is(err, inmemory.ErrMissingEmbeddingModel) {
		t.Fatalf("NewStore typed nil error = %v, want ErrMissingEmbeddingModel", err)
	}
}

type nilEmbeddingModel struct{}

func (*nilEmbeddingModel) Call(context.Context, *embedding.Request) (*embedding.Response, error) {
	return nil, nil
}

func TestStore_SearchOnEmpty(t *testing.T) {
	store := newStore(t)
	retrieveReq := &vectorstore.SearchRequest{Query: "anything", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	got, err := search(store, t.Context(), retrieveReq)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d, want 0", len(got))
	}
}

func TestStore_IndexUpsertsExistingID(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()

	first := []*document.Document{mustDoc(t, "1", "original", map[string]any{"v": 1})}
	createReq := first
	_ = store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq})

	second := []*document.Document{mustDoc(t, "1", "updated", map[string]any{"v": 2})}
	updateReq := second
	if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: updateReq}); err != nil {
		t.Fatalf("Index upsert: %v", err)
	}

	if got := store.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1 (upsert)", got)
	}
}

func TestSimilarity_CosineSelfIsOne(t *testing.T) {
	v := []float64{1, 2, 3, 4}
	if got := inmemory.CosineSimilarity(v, v); got < 0.999 {
		t.Fatalf("CosineSimilarity(v, v) = %v, want ≈1", got)
	}
}

func TestSimilarity_EuclideanSelfIsOne(t *testing.T) {
	v := []float64{1, 2, 3}
	if got := inmemory.EuclideanSimilarity(v, v); got < 0.999 {
		t.Fatalf("EuclideanSimilarity(v, v) = %v, want 1", got)
	}
}

func TestStore_SearchIsNull(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()
	docs := []*document.Document{
		mustDoc(t, "1", "alpha", map[string]any{"category": "a"}),   // no "owner"
		mustDoc(t, "2", "bravo", map[string]any{"owner": "alice"}),  // has "owner"
		mustDoc(t, "3", "charlie", map[string]any{"category": "c"}), // no "owner"
	}
	createReq := docs
	if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// IS NULL: matches docs missing "owner".
	expr, err := filter.Parse(`owner is null`)
	if err != nil {
		t.Fatalf("Parse(is null): %v", err)
	}
	req := &vectorstore.SearchRequest{Query: "x", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	req.Options.Filter = expr
	req.Options.TopK = 10
	got, err := search(store, ctx, req)
	if err != nil {
		t.Fatalf("Search(is null): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("is null matched %d docs, want 2 (ids 1,3)", len(got))
	}

	// IS NOT NULL: matches the doc that has "owner".
	expr2, err := filter.Parse(`owner is not null`)
	if err != nil {
		t.Fatalf("Parse(is not null): %v", err)
	}
	req2 := &vectorstore.SearchRequest{Query: "x", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	req2.Options.Filter = expr2
	req2.Options.TopK = 10
	got2, err := search(store, ctx, req2)
	if err != nil {
		t.Fatalf("Search(is not null): %v", err)
	}
	if len(got2) != 1 || got2[0].Document.ID != "2" {
		t.Fatalf("is not null = %+v, want [id 2]", got2)
	}
}

func TestStore_SearchNotIn(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()
	docs := []*document.Document{
		mustDoc(t, "1", "alpha", map[string]any{"category": "a"}),
		mustDoc(t, "2", "bravo", map[string]any{"category": "b"}),
		mustDoc(t, "3", "charlie", map[string]any{"category": "c"}),
	}
	createReq := docs
	if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	expr, err := filter.Parse(`category not in ('a', 'b')`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	req := &vectorstore.SearchRequest{Query: "x", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	req.Options.Filter = expr
	req.Options.TopK = 10
	got, err := search(store, ctx, req)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Document.ID != "3" {
		ids := make([]string, 0, len(got))
		for _, d := range got {
			ids = append(ids, d.Document.ID)
		}
		t.Fatalf("not in ('a','b') matched %v, want [3]", ids)
	}
}

func TestStore_DeleteIDs(t *testing.T) {
	store := newStore(t)
	ctx := t.Context()
	docs := []*document.Document{
		mustDoc(t, "1", "a", map[string]any{"k": "v"}),
		mustDoc(t, "2", "b", map[string]any{"k": "v"}),
		mustDoc(t, "3", "c", map[string]any{"k": "v"}),
	}
	createReq := docs
	if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: createReq}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Empty slice is a no-op; an unknown id is ignored.
	if err := store.DeleteIDs(ctx, nil); err != nil {
		t.Fatalf("DeleteIDs(nil): %v", err)
	}
	if err := store.DeleteIDs(ctx, []string{"1", "missing"}); err != nil {
		t.Fatalf("DeleteIDs: %v", err)
	}

	req := &vectorstore.SearchRequest{Query: "x", Options: vectorstore.SearchOptions{TopK: vectorstore.DefaultTopK}}
	req.Options.TopK = 10
	got, err := search(store, ctx, req)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("after DeleteIDs([1]), have %d docs, want 2 (2,3)", len(got))
	}
	for _, match := range got {
		if match.Document.ID == "1" {
			t.Fatal("id 1 should have been deleted")
		}
	}
}
