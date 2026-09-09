package elasticsearch

import (
	"context"
	"testing"

	elasticsearchsdk "github.com/elastic/go-elasticsearch/v8"

	"github.com/Tangerg/scope/core/embedding"
)

// num_candidates is derived from the requested result count by this multiplier
// and nothing else, and Elasticsearch answers "[num_candidates] cannot be less
// than [k]". A multiplier under 1 therefore builds a store whose every search
// is rejected, which is worth saying at wiring rather than on first query.
func TestStoreConfigRejectsAMultiplierBelowOne(t *testing.T) {
	for _, multiplier := range []float64{0.5, 0.999} {
		config := newValidConfig(t)
		config.NumCandidatesMultiplier = multiplier
		if err := config.Validate(); err == nil {
			t.Fatalf("Validate multiplier %g = nil, want the store refused", multiplier)
		}
	}
	// Zero still selects the documented default rather than being refused.
	config := newValidConfig(t)
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want the default multiplier accepted", err)
	}
}

func newValidConfig(t *testing.T) StoreConfig {
	t.Helper()
	return StoreConfig{
		Client: new(elasticsearchsdk.Client),
		EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
			t.Fatal("configuration validation invoked the embedding model")
			return nil, nil
		}),
		DocumentBatcher: new(configBatcher),
	}
}

func TestStoreConfigRejectsCollidingDocumentFields(t *testing.T) {
	for _, fields := range [][3]string{
		{"body", "body", "metadata"},
		{"body", "vector", "body"},
		{"body", "vector", "vector"},
		{DefaultMetadataField, "", ""},
	} {
		config := StoreConfig{
			Client: new(elasticsearchsdk.Client),
			EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
				t.Fatal("configuration validation invoked the embedding model")
				return nil, nil
			}),
			DocumentBatcher: new(configBatcher),
			ContentField:    fields[0], EmbeddingField: fields[1], MetadataField: fields[2],
		}
		if err := config.Validate(); err == nil || err.Error() != "elasticsearch: ContentField, EmbeddingField, and MetadataField must be distinct" {
			t.Fatalf("Validate fields %v = %v; want field collision rejection", fields, err)
		}
	}
}
