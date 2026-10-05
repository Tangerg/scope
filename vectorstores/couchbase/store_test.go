package couchbase

import (
	"context"
	"errors"
	"testing"

	"github.com/couchbase/gocb/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
)

type configBatcher struct{}

func (c *configBatcher) Batch(ctx context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{docs}, ctx.Err()
}

func TestStoreConfigValidation(t *testing.T) {
	valid := StoreConfig{Cluster: new(gocb.Cluster), BucketName: "bucket-name", EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) { return nil, nil }), DocumentBatcher: new(configBatcher)}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*StoreConfig){
		func(c *StoreConfig) { c.Cluster = nil },
		func(c *StoreConfig) { c.BucketName = "" },
		func(c *StoreConfig) { c.BucketName = "bucket`" },
		func(c *StoreConfig) { c.ScopeName = "scope.x" },
		func(c *StoreConfig) { c.CollectionName = "collection x" },
		func(c *StoreConfig) { c.EmbeddingModel = nil },
		func(c *StoreConfig) { c.EmbeddingModel = embedding.ModelFunc(nil) },
		func(c *StoreConfig) { c.DocumentBatcher = nil },
		func(c *StoreConfig) { c.DocumentBatcher = (*configBatcher)(nil) },
		func(c *StoreConfig) { c.Similarity = Similarity("wrong") },
	} {
		config := valid
		change(&config)
		if err := config.Validate(); err == nil {
			t.Fatal("invalid config accepted")
		}
		if s, err := NewStore(t.Context(), config); s != nil || err == nil {
			t.Fatal("invalid constructor reached I/O")
		}
	}
	config := valid
	config.applyDefaults()
	if config.ScopeName != DefaultScopeName || config.CollectionName != DefaultCollectionName || config.Similarity != DefaultSimilarity {
		t.Fatal(config)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s, err := NewStore(ctx, valid); s != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled constructor reached SDK: %v", err)
	}
}
