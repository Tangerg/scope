package couchbase

import (
	"context"
	"errors"
	"testing"

	"github.com/couchbase/gocb/v2"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestStoreConfigValidation(t *testing.T) {
	valid := StoreConfig{Cluster: new(gocb.Cluster), BucketName: "bucket-name", Space: "fixture", Dimensions: 2}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*StoreConfig){
		func(c *StoreConfig) { c.Cluster = nil },
		func(c *StoreConfig) { c.BucketName = "" },
		func(c *StoreConfig) { c.BucketName = "bucket`" },
		func(c *StoreConfig) { c.ScopeName = "scope.x" },
		func(c *StoreConfig) { c.Dimensions = 0 }, func(c *StoreConfig) { c.Space = "" },
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
	if config.ScopeName != DefaultScopeName || config.CollectionName() != config.Space.StorageKey()+"_2" || config.Similarity != DefaultSimilarity {
		t.Fatal(config)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s, err := NewStore(ctx, valid); s != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled constructor reached SDK: %v", err)
	}
}

func TestCollectionIdentityCannotMixSpacesOrWidths(t *testing.T) {
	a := StoreConfig{Space: "embedder/v1", Dimensions: 2}
	b := a
	b.Space = "embedder/v2"
	c := a
	c.Dimensions = 3
	if a.CollectionName() == b.CollectionName() || a.CollectionName() == c.CollectionName() {
		t.Fatal("physical collection shared by incompatible bindings")
	}
	s := &Store{space: a.Space, dimensions: a.Dimensions}
	if _, ok := any(s).(vectorstore.Querier); ok {
		t.Fatal("primary index advertised as vector index")
	}
	if _, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: b.Space, Values: []float64{1, 0}}, vectorstore.ScanBudget{}); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatal(err)
	}
	if _, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: a.Space, Values: []float64{1, 0}}, vectorstore.ScanBudget{}); err == nil {
		t.Fatal("unbudgeted scan")
	}
}
