package typesense

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestFormatVectorQueryIncludesExplicitHybridAlpha(t *testing.T) {
	t.Parallel()

	alpha := float32(0.75)
	got := formatVectorQuery([]float32{1, 0.5}, 3, &alpha)
	want := "embedding:([1,0.5], k: 3, alpha: 0.75)"
	if got != want {
		t.Fatalf("formatVectorQuery() = %q, want %q", got, want)
	}
}

func TestHybridMatchAcceptsLexicalOnlyHit(t *testing.T) {
	t.Parallel()

	document := &storedDocument{ID: "one", Content: "content"}
	match, err := toMatch(searchHit{Document: document}, vectorstore.SearchModeHybrid, 1)
	if err != nil {
		t.Fatal(err)
	}
	if match.Score != scoreFromRank(1) {
		t.Fatalf("score = %v, want %v", match.Score, scoreFromRank(1))
	}
}

func TestSearchParametersMapHybridModeOnce(t *testing.T) {
	t.Parallel()

	alpha := float32(0.75)
	store := &Store{hybridAlpha: &alpha}
	request := &vectorstore.SearchRequest{
		Query: "capital of France", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, TopK: 3},
	}
	params := store.searchParameters(request, []float32{1, 0.5})
	if params.Q == nil || *params.Q != request.Query {
		t.Fatalf("Q = %v, want %q", params.Q, request.Query)
	}
	if params.QueryBy == nil || *params.QueryBy != contentField {
		t.Fatalf("QueryBy = %v, want %q", params.QueryBy, contentField)
	}
	if params.VectorQuery == nil || *params.VectorQuery != "embedding:([1,0.5], k: 3, alpha: 0.75)" {
		t.Fatalf("VectorQuery = %v", params.VectorQuery)
	}
}
