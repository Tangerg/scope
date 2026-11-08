package vector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/rag"
	ragvector "github.com/Tangerg/scope/rag/vector"
)

type duplicateSource struct{}

func (duplicateSource) CheckQuery(context.Context, vectorstore.SpaceID, vectorstore.SearchOptions) error {
	return nil
}

func (duplicateSource) Query(context.Context, *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	return duplicateResponse(), nil
}

func duplicateResponse() *vectorstore.SearchResponse {
	return &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{
		{Document: &document.Document{ID: "same-id", Text: "first version"}, Score: 0.9},
		{Document: &document.Document{ID: "same-id", Text: "conflicting second version"}, Score: 0.8},
	}}
}

func TestRetrieverRejectsConflictingDocumentIdentities(t *testing.T) {
	model := embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
	})
	binding := embeddingclient.Config{Model: model}
	encoder, err := ragvector.NewEncoder(ragvector.EncoderConfig{Space: "audit-space", Documents: binding, Queries: binding})
	if err != nil {
		t.Fatal(err)
	}
	retriever, err := ragvector.NewRetriever(ragvector.RetrieverConfig{Encoder: encoder, VectorStore: duplicateSource{}})
	if err != nil {
		t.Fatal(err)
	}
	query, err := rag.NewQuery("audit query")
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := retriever.Retrieve(t.Context(), query)
	if !errors.Is(err, vectorstore.ErrInvalidResponse) || len(candidates) != 0 {
		t.Fatalf("conflicting identities escaped response boundary: candidates=%#v error=%v", candidates, err)
	}
}
