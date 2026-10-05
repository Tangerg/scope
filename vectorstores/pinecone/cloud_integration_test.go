//go:build integration

package pinecone

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	pineconesdk "github.com/pinecone-io/go-pinecone/v4/pinecone"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestCloudNativeConditionalDeleteRetainsChangedMetadata(t *testing.T) {
	key, name := os.Getenv("SCOPE_PINECONE_API_KEY"), os.Getenv("SCOPE_PINECONE_INDEX_NAME")
	if key == "" || name == "" {
		t.Fatal("current Pinecone cloud key and ready 2-dimensional serverless index are required")
	}
	client, err := pineconesdk.NewClient(pineconesdk.NewClientParams{ApiKey: key})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.Context(), StoreConfig{Client: client, IndexName: name, Namespace: "scope-test-" + rand.Text(), EmbeddingModel: fixtureModel(), DocumentBatcher: fixtureBatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	native := store.index.(*pineconesdk.IndexConnection)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if deleteErr := native.DeleteAllVectorsInNamespace(ctx); deleteErr != nil {
			t.Error(deleteErr)
		}
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	facts, err := metadata.FromValues(map[string]any{"value": "a"})
	if err != nil {
		t.Fatal(err)
	}
	installDocuments(t, store, &document.Document{ID: "changed", Text: "text", Metadata: facts}, &document.Document{ID: "unchanged", Text: "text", Metadata: facts})
	waitForCloudRecords(t, native, map[string]string{"changed": `{"value":"a"}`, "unchanged": `{"value":"a"}`})
	store.index = &changedNativeIndex{indexConnection: native, connection: native, afterUpdate: func(ctx context.Context) error {
		visible, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			page, queryErr := native.QueryByVectorValues(visible, &pineconesdk.QueryByVectorValuesRequest{Vector: []float32{1, 0}, TopK: 2, MetadataFilter: metadataSelection([]string{`{"value":"b"}`}), IncludeMetadata: true})
			if queryErr != nil {
				return queryErr
			}
			for _, hit := range page.Matches {
				if hit.Vector.Id == "changed" {
					return nil
				}
			}
			select {
			case <-visible.Done():
				return fmt.Errorf("native metadata update visibility: %w", context.Cause(visible))
			case <-ticker.C:
			}
		}
	}}
	if err = store.DeleteWhere(t.Context(), filter.EQ("value", "a")); err != nil {
		t.Fatal(err)
	}
	waitForCloudRecords(t, native, map[string]string{"changed": `{"value":"b"}`})
}

func waitForCloudRecords(t *testing.T, native *pineconesdk.IndexConnection, expected map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := native.FetchVectors(ctx, []string{"changed", "unchanged"})
		if err != nil {
			t.Fatal(err)
		}
		matched := len(page.Vectors) == len(expected)
		for id, payload := range expected {
			point := page.Vectors[id]
			if point == nil || point.Metadata == nil || point.Metadata.Fields[metadataField].GetStringValue() != payload {
				matched = false
			}
		}
		if matched {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("native records did not reach expected visibility")
		case <-ticker.C:
		}
	}
}
