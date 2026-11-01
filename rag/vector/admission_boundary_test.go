package vector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/rag"
	"github.com/Tangerg/scope/rag/vector"
)

type rewritingSource struct{ fakeVectorSearcher }

func (r *rewritingSource) Query(ctx context.Context, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	q.Options.Filter = nil
	return r.fakeVectorSearcher.Query(ctx, q)
}

func TestSourceCannotRedefineTheTenantPredicateForItsReply(t *testing.T) {
	s := new(rewritingSource)
	r, err := vector.NewRetriever(vector.RetrieverConfig{Encoder: demoEncoder(), VectorStore: s, FilterFunc: func(context.Context, rag.Query) (filter.Predicate, error) { return filter.EQ("tenant", "red"), nil }})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := rag.NewQuery("red records")
	if hits, callErr := r.Retrieve(t.Context(), q); callErr == nil || hits != nil {
		t.Fatalf("wrong-tenant source reply admitted: %v, %v", hits, callErr)
	}
}

func TestEncoderKeepsOriginalCardinalityAndOwnsReturnedCoordinates(t *testing.T) {
	coords := []float64{7}
	corrupt := false
	model := embedding.ModelFunc(func(_ context.Context, q *embedding.Request) (*embedding.Response, error) {
		if corrupt {
			q.Texts = append(q.Texts, "foreign")
			return embedding.NewResponse([]*embedding.Output{{Embedding: coords}, {Embedding: coords}}, nil)
		}
		return embedding.NewResponse([]*embedding.Output{{Embedding: coords}}, nil)
	})
	e, err := vector.NewEncoder(vector.EncoderConfig{Space: "v1", Documents: vector.Encoding{Model: model}, Queries: vector.Encoding{Model: model}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.EncodeDocuments(t.Context(), []string{"source"})
	if err != nil {
		t.Fatal(err)
	}
	coords[0] = 9
	if got[0][0] != 7 {
		t.Fatal("model reused its storage and changed published coordinates")
	}
	corrupt = true
	if got, callErr := e.EncodeDocuments(t.Context(), []string{"source"}); !errors.Is(callErr, embedding.ErrInvalidResponse) || got != nil {
		t.Fatal(got, callErr)
	}
}
