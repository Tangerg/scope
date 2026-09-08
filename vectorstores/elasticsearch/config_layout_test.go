package elasticsearch

import (
	"context"
	"testing"

	elasticsearchsdk "github.com/elastic/go-elasticsearch/v8"

	"github.com/Tangerg/scope/core/embedding"
)

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
