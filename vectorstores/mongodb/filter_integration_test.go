//go:build integration

package mongodb

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// Explicitly running this test creates and removes an isolated database.
func TestLiveFilterConformance(t *testing.T) {
	uri := os.Getenv("SCOPE_MONGODB_URI")
	if uri == "" {
		t.Fatal("SCOPE_MONGODB_URI is required with -tags=integration")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database("scope_filter_" + rand.Text())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if err := db.Drop(ctx); err != nil {
			t.Errorf("cleanup database: %v", err)
		}
		_ = client.Disconnect(ctx)
	})
	if err := db.CreateCollection(t.Context(), "documents"); err != nil {
		t.Fatal(err)
	}
	collection := db.Collection("documents")
	store, err := NewStore(t.Context(), StoreConfig{Collection: collection, EmbeddingModel: constantModel{}, DocumentBatcher: upsertBatcher{}, Dimensions: 2, InitializeSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		cursor, err := collection.SearchIndexes().List(ctx, options.SearchIndexes().SetName(DefaultVectorIndexName))
		if err != nil {
			t.Fatal(err)
		}
		ready := false
		for cursor.Next(ctx) {
			var info struct {
				Queryable bool `bson:"queryable"`
			}
			if err := cursor.Decode(&info); err != nil {
				_ = cursor.Close(ctx)
				t.Fatal(err)
			}
			ready = info.Queryable
		}
		if err := cursor.Err(); err != nil {
			_ = cursor.Close(ctx)
			t.Fatal(err)
		}
		if err := cursor.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		if _, err := collection.DeleteMany(ctx, bson.M{}); err != nil {
			return nil, err
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
