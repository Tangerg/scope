package vectorstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/rag"
	ragvectorstore "github.com/Tangerg/scope/rag/vectorstore"
)

// fakeVectorSearcher captures the request the retriever issues so
// tests can assert that filters / topK / minScore are wired through.
type fakeVectorSearcher struct {
	got         *vectorstore.SearchRequest
	err         error
	nilResponse bool
}

func (f *fakeVectorSearcher) Search(_ context.Context, req *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	if f.nilResponse {
		return nil, nil
	}
	doc, _ := document.NewDocument("hit", nil)
	doc.ID = "hit"
	return &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{{Document: doc, Score: 0.75}}}, nil
}

func TestNewVectorStoreRetrieverRejectsInvalidConfig(t *testing.T) {
	if _, err := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{}); err == nil {
		t.Fatal("nil config must error")
	}
	if _, err := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
		VectorStore: &fakeVectorSearcher{},
		MinScore:    1.5,
	}); err == nil {
		t.Fatal("out-of-range MinScore must error")
	}
	if _, err := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
		VectorStore: &fakeVectorSearcher{}, SearchMode: vectorstore.SearchModeHybrid, MinScore: 0.5,
	}); err == nil {
		t.Fatal("hybrid MinScore must error")
	}
}

func TestRetrieverAppliesTopKAndMinScore(t *testing.T) {
	store := &fakeVectorSearcher{}
	r, err := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
		VectorStore: store,
		TopK:        7,
		MinScore:    0.42,
	})
	if err != nil {
		t.Fatal(err)
	}

	q, _ := rag.NewQuery("hi")
	if _, err := r.Retrieve(t.Context(), q); err != nil {
		t.Fatal(err)
	}

	if store.got.Options.TopK != 7 {
		t.Fatalf("TopK = %d, want 7", store.got.Options.TopK)
	}
	if store.got.Options.MinScore != 0.42 {
		t.Fatalf("MinScore = %f, want 0.42", store.got.Options.MinScore)
	}
}

func TestRetrieverForwardsHybridMode(t *testing.T) {
	store := &fakeVectorSearcher{}
	retriever, err := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
		VectorStore: store, SearchMode: vectorstore.SearchModeHybrid,
	})
	if err != nil {
		t.Fatal(err)
	}
	query, _ := rag.NewQuery("hi")
	if _, err := retriever.Retrieve(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	if store.got.Options.EffectiveMode() != vectorstore.SearchModeHybrid {
		t.Fatalf("search mode = %q, want hybrid", store.got.Options.EffectiveMode())
	}
}

func TestRetrieverPerQueryFilterOverridesFunc(t *testing.T) {
	store := &fakeVectorSearcher{}
	funcCalls := 0

	r, err := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
		VectorStore: store,
		FilterFunc: func(_ context.Context, _ rag.Query) (filter.Predicate, error) {
			funcCalls++
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	q, _ := rag.NewQuery("hi")
	parsed, err := filter.Parse(`category == 'tech'`)
	if err != nil {
		t.Fatal(err)
	}
	q, err = q.WithValue(ragvectorstore.FilterValueKey(), parsed)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Retrieve(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if funcCalls != 0 {
		t.Fatal("per-query filter must override FilterFunc")
	}
	if store.got.Options.Filter == nil {
		t.Fatal("filter was not threaded into the retrieval request")
	}
}

func TestRetrieverUsesParsedQueryFilter(t *testing.T) {
	store := &fakeVectorSearcher{}
	r, _ := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
		VectorStore: store,
	})

	q, _ := rag.NewQuery("hi")
	parsed, err := filter.Parse(`year >= 2020`)
	if err != nil {
		t.Fatal(err)
	}
	q, err = q.WithValue(ragvectorstore.FilterValueKey(), parsed)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Retrieve(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if store.got.Options.Filter == nil {
		t.Fatal("string filter was not parsed and threaded through")
	}
}

func TestRetrieverPropagatesError(t *testing.T) {
	want := errors.New("boom")
	store := &fakeVectorSearcher{err: want}
	r, _ := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{VectorStore: store})

	q, _ := rag.NewQuery("hi")
	if _, err := r.Retrieve(t.Context(), q); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetrieverRejectsNilVectorStoreResponse(t *testing.T) {
	store := &fakeVectorSearcher{nilResponse: true}
	r, _ := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{VectorStore: store})
	query, _ := rag.NewQuery("hi")

	if _, err := r.Retrieve(t.Context(), query); !errors.Is(err, vectorstore.ErrInvalidResponse) {
		t.Fatalf("nil response error = %v", err)
	}
}

func TestRetrieverRejectsZeroQuery(t *testing.T) {
	r, _ := ragvectorstore.NewRetriever(ragvectorstore.RetrieverConfig{
		VectorStore: &fakeVectorSearcher{},
	})
	if _, err := r.Retrieve(t.Context(), rag.Query{}); err == nil {
		t.Fatal("zero query must error")
	}
}

func TestVectorStoreRetrieverPreservesInvalidOptions(t *testing.T) {
	for name, config := range map[string]ragvectorstore.RetrieverConfig{
		"negative top k": {TopK: -1}, "invalid score": {MinScore: 2},
		"unknown mode": {SearchMode: "unknown"},
		"hybrid score": {SearchMode: vectorstore.SearchModeHybrid, MinScore: 0.5},
	} {
		t.Run(name, func(t *testing.T) {
			config.VectorStore = &fakeVectorSearcher{}
			if _, err := ragvectorstore.NewRetriever(config); !errors.Is(err, vectorstore.ErrInvalidOptions) {
				t.Fatalf("NewRetriever error = %v, want ErrInvalidOptions", err)
			}
		})
	}
}
