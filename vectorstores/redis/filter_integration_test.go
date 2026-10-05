//go:build integration

package redis

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// Explicitly running this test creates and removes an isolated Redis index.
func TestLiveFilterConformance(t *testing.T) {
	store, _, _ := liveStore(t, "COSINE", constantModel())
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
	var mismatch string
	for {
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: max(len(docs), 10)}})
		if err != nil {
			return fmt.Errorf("%s: %w", mismatch, err)
		}
		visible := len(response.Results) == len(docs)
		mismatch = fmt.Sprintf("wanted %d documents, received %d", len(docs), len(response.Results))
		for _, doc := range docs {
			found := false
			for _, hit := range response.Results {
				if hit.Document.ID == doc.ID && hit.Document.Metadata.Equal(doc.Metadata) {
					found = true
					break
				}
				if hit.Document.ID == doc.ID {
					mismatch = fmt.Sprintf("document %q metadata: want %s, received %s", doc.ID, doc.Metadata, hit.Document.Metadata)
				}
			}
			visible = visible && found
		}
		if visible {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("fixture index visibility: %s: %w", mismatch, ctx.Err())
		case <-ticker.C:
		}
	}
}
