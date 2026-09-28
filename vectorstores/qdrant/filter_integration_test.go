//go:build integration

package qdrant

import (
	"context"
	"crypto/rand"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type integrationBatcher struct{}

func (integrationBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{docs}, nil
}

// Explicitly running this test creates and removes an isolated collection.
func TestLiveFilterConformance(t *testing.T) {
	address := os.Getenv("SCOPE_QDRANT_ADDR")
	if address == "" {
		t.Fatal("SCOPE_QDRANT_ADDR is required with -tags=integration")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	client, err := qdrantclient.NewClient(&qdrantclient.Config{Host: host, Port: port, APIKey: os.Getenv("SCOPE_QDRANT_API_KEY"), UseTLS: os.Getenv("SCOPE_QDRANT_TLS") == "true"})
	if err != nil {
		t.Fatal(err)
	}
	name := "scope_filter_" + rand.Text()
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for index := range outputs {
			outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, CollectionName: name, EmbeddingModel: model, DocumentBatcher: integrationBatcher{}, DistanceMetric: DistanceCosine, Dimensions: 2, InitializeSchema: true})
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if err := client.DeleteCollection(ctx, name); err != nil {
			t.Errorf("cleanup collection: %v", err)
		}
		_ = client.Close()
	})
	var previous []string
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		if err := store.DeleteIDs(ctx, previous); err != nil {
			return nil, err
		}
		previous = nil
		for _, doc := range docs {
			previous = append(previous, doc.ID)
		}
		if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
			return nil, err
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
}
