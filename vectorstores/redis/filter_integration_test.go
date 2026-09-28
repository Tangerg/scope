//go:build integration

package redis

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// Explicitly running this test creates and removes an isolated Redis index.
func TestLiveFilterConformance(t *testing.T) {
	address := os.Getenv("SCOPE_REDIS_ADDR")
	if address == "" {
		t.Fatal("SCOPE_REDIS_ADDR is required with -tags=integration")
	}
	client := goredis.NewClient(&goredis.Options{Addr: address, Username: os.Getenv("SCOPE_REDIS_USERNAME"), Password: os.Getenv("SCOPE_REDIS_PASSWORD")})
	name := "scope-filter-" + rand.Text()
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for index := range outputs {
			outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, IndexName: name, KeyPrefix: name + ":", EmbeddingModel: model, DocumentBatcher: ownershipBatcher{}, Dimensions: 2, IndexAlgorithm: AlgorithmFlat, InitializeSchema: true})
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if err := client.Do(ctx, "FT.DROPINDEX", name, "DD").Err(); err != nil {
			t.Errorf("cleanup index: %v", err)
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
		if err := awaitVisibleDocuments(ctx, store, docs); err != nil {
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

func awaitVisibleDocuments(ctx context.Context, store *Store, docs []*document.Document) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: max(len(docs), 10)}})
		if err != nil {
			return err
		}
		visible := len(response.Results) == len(docs)
		for _, doc := range docs {
			found := false
			for _, hit := range response.Results {
				if hit.Document.ID == doc.ID && hit.Document.Metadata.Equal(doc.Metadata) {
					found = true
					break
				}
			}
			visible = visible && found
		}
		if visible {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("fixture index visibility: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
