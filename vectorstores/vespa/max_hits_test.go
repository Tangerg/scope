package vespa

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Vespa documents that "hits is capped at maxHits, default 400", and applies
// the cap by trimming the result rather than answering an error. A search for
// more than that came back short with nothing to say it had been truncated --
// the caller could not tell a capped result from an exhausted one.
//
// The delete path already knew about the ceiling and paged within it; the
// search path did not, which is how one known limit ended up handled in one
// place and ignored in the other.
func TestSearchRefusesATopKVespaWouldSilentlyTrim(t *testing.T) {
	t.Parallel()

	store := newMaxHitsStore(t, 0)
	request, err := vectorstore.NewSearchRequest("query")
	if err != nil {
		t.Fatal(err)
	}
	request.Options.TopK = DefaultMaxHits + 1

	_, err = store.Search(t.Context(), request)
	if err == nil {
		t.Fatal("Search() = nil error, want the oversized TopK refused")
	}
	if !strings.Contains(err.Error(), "maxHits") {
		t.Fatalf("Search() = %v, want an error naming maxHits", err)
	}
}

// maxHits is a query-profile value the application owns, so a deployment that
// raised it says so here rather than being held to Vespa's default.
func TestSearchHonorsAConfiguredMaxHits(t *testing.T) {
	t.Parallel()

	store := newMaxHitsStore(t, DefaultMaxHits*2)
	request, err := vectorstore.NewSearchRequest("query")
	if err != nil {
		t.Fatal(err)
	}
	request.Options.TopK = DefaultMaxHits + 1

	// The endpoint points nowhere, so reaching the transport is what shows the
	// ceiling let this TopK through.
	_, err = store.Search(t.Context(), request)
	if err == nil {
		t.Fatal("Search() = nil error, want the request to reach the transport")
	}
	if strings.Contains(err.Error(), "maxHits") {
		t.Fatalf("Search() = %v, want the configured ceiling to allow this TopK", err)
	}
}

func newMaxHitsStore(t *testing.T, maxHits int) *Store {
	t.Helper()

	store, err := NewStore(t.Context(), StoreConfig{
		Endpoint:        "http://127.0.0.1:1",
		SchemaName:      "documents",
		Namespace:       "documents",
		RankingProfile:  "closeness_profile",
		MaxHits:         maxHits,
		EmbeddingModel:  maxHitsEmbeddingModel{},
		DocumentBatcher: maxHitsBatcher{},
		HTTPClient:      &http.Client{},
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

type maxHitsEmbeddingModel struct{}

func (maxHitsEmbeddingModel) Call(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, len(request.Texts))
	for index := range outputs {
		outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
	}
	return embedding.NewResponse(outputs, nil)
}

type maxHitsBatcher struct{}

func (maxHitsBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	return [][]*document.Document{documents}, nil
}
