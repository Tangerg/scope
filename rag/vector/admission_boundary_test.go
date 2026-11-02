package vector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
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
	e, err := vector.NewEncoder(vector.EncoderConfig{Space: "v1", Documents: embeddingclient.Config{Model: model}, Queries: embeddingclient.Config{Model: model}})
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

type predicateRewriter struct {
	fakeVectorSearcher
	onAdmission bool
}

func (p *predicateRewriter) CheckQuery(_ context.Context, _ vectorstore.SpaceID, options vectorstore.SearchOptions) error {
	if p.onAdmission {
		*options.Filter.(*filter.BinaryExpr) = *filter.EQ("tenant", "blue")
	}
	return nil
}

func (p *predicateRewriter) Query(ctx context.Context, query *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if !p.onAdmission {
		*query.Options.Filter.(*filter.BinaryExpr) = *filter.EQ("tenant", "blue")
	}
	return p.fakeVectorSearcher.Query(ctx, query)
}

func TestRetrieverIsolatesPredicateAtEachSourceBoundary(t *testing.T) {
	for _, admission := range []bool{true, false} {
		policy := filter.EQ("tenant", "red")
		source := &predicateRewriter{onAdmission: admission}
		if err := source.metadata.Set("tenant", "blue"); err != nil {
			t.Fatal(err)
		}
		r, err := vector.NewRetriever(vector.RetrieverConfig{Encoder: demoEncoder(), VectorStore: source, FilterFunc: func(context.Context, rag.Query) (filter.Predicate, error) { return policy, nil }})
		if err != nil {
			t.Fatal(err)
		}
		q, _ := rag.NewQuery("records")
		if got, err := r.Retrieve(t.Context(), q); !errors.Is(err, vectorstore.ErrInvalidResponse) || got != nil {
			t.Fatalf("admission=%t: cross-tenant result: %v, %v", admission, got, err)
		}
		if !policy.Equal(filter.EQ("tenant", "red")) {
			t.Fatalf("admission=%t: caller's tenant policy changed: %v", admission, policy)
		}
	}
}

func TestRetrieverKeepsGrantedFilterThroughEncoding(t *testing.T) {
	policy := filter.EQ("tenant", "red")
	binding := embeddingclient.Config{Model: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		*policy = *filter.EQ("tenant", "blue")
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1}}}, nil)
	})}
	encoder, err := vector.NewEncoder(vector.EncoderConfig{Space: "v1", Documents: binding, Queries: binding})
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeVectorSearcher{}
	if err = source.metadata.Set("tenant", "blue"); err != nil {
		t.Fatal(err)
	}
	retriever, err := vector.NewRetriever(vector.RetrieverConfig{Encoder: encoder, VectorStore: source, FilterFunc: func(context.Context, rag.Query) (filter.Predicate, error) { return policy, nil }})
	if err != nil {
		t.Fatal(err)
	}
	query, _ := rag.NewQuery("records")
	if got, err := retriever.Retrieve(t.Context(), query); got != nil || !errors.Is(err, vectorstore.ErrInvalidResponse) {
		t.Fatalf("encoding replaced granted policy: %v, %v", got, err)
	}
}

func TestEncodedQueryOwnsItsSearchPolicy(t *testing.T) {
	policy := filter.EQ("tenant", "red")
	query, err := demoEncoder().EncodeQuery(t.Context(), "records", vectorstore.SearchOptions{Filter: policy})
	if err != nil {
		t.Fatal(err)
	}
	*policy = *filter.EQ("tenant", "blue")
	if !query.Options.Filter.Equal(filter.EQ("tenant", "red")) {
		t.Fatal("caller replaced the encoded query's policy")
	}
}
