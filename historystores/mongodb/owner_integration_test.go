//go:build integration

package mongodb

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/history/storetest"
)

func TestNativeAtomicHistory(t *testing.T) {
	uri := os.Getenv("SCOPE_MONGODB_URI")
	if uri == "" {
		t.Fatal("SCOPE_MONGODB_URI must identify an isolated replica set or sharded test deployment")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(context.Background()); err != nil {
			t.Error(err)
		}
	})
	database := client.Database("scope_history_" + rand.Text())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := database.Drop(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := database.CreateCollection(t.Context(), "history", options.CreateCollection().SetCollation(&options.Collation{Locale: "en", Strength: 1})); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.Context(), StoreConfig{Collection: database.Collection("history"), InitializeSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	storetest.RunAtomic(t, store)
}
