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

func liveMongoStore(t *testing.T, metric string) (*Store, *mongo.Collection) {
	t.Helper()
	uri := os.Getenv("SCOPE_MONGODB_URI")
	if uri == "" {
		t.Fatal("SCOPE_MONGODB_URI is required with -tags=integration")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if closeErr := client.Disconnect(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	db := client.Database("scope_native_" + rand.Text())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if dropErr := db.Drop(ctx); dropErr != nil {
			t.Error(dropErr)
		}
	})
	if err = db.CreateCollection(t.Context(), "documents", options.CreateCollection().SetCollation(&options.Collation{Locale: "en", Strength: 2})); err != nil {
		t.Fatal(err)
	}
	collection := db.Collection("documents")
	index := mongo.SearchIndexModel{Definition: bson.M{"fields": bson.A{bson.M{"type": "vector", "path": embeddingField, "numDimensions": 2, "similarity": metric}, bson.M{"type": "filter", "path": idField}}}, Options: options.SearchIndexes().SetName(DefaultVectorIndexName).SetType("vectorSearch")}
	if _, err = collection.SearchIndexes().CreateOne(t.Context(), index); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		cursor, listErr := collection.SearchIndexes().List(ctx, options.SearchIndexes().SetName(DefaultVectorIndexName))
		if listErr != nil {
			t.Fatal(listErr)
		}
		var policies []bson.M
		if readErr := cursor.All(ctx, &policies); readErr != nil {
			t.Fatal(readErr)
		}
		if len(policies) == 1 && policies[0]["status"] == "READY" && policies[0]["queryable"] == true {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	store, err := NewStore(t.Context(), StoreConfig{Collection: collection, EmbeddingModel: constantModel{}, DocumentBatcher: upsertBatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	return store, collection
}

func TestLiveFilterConformance(t *testing.T) {
	for _, metric := range []string{"cosine", "euclidean", "dotProduct"} {
		t.Run(metric, func(t *testing.T) {
			store, collection := liveMongoStore(t, metric)
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
		})
	}
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
