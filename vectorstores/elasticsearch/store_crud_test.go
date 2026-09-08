package elasticsearch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	elasticsearchsdk "github.com/elastic/go-elasticsearch/v8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
)

type configBatcher struct{}

func (*configBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func TestStoreConfigRejectsTypedNilDependencies(t *testing.T) {
	config := StoreConfig{
		Client: new(elasticsearchsdk.Client),
		EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
			t.Fatal("configuration validation invoked the embedding model")
			return nil, nil
		}),
		DocumentBatcher: new(configBatcher),
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*StoreConfig)
		want   string
	}{
		{name: "model", change: func(config *StoreConfig) { config.EmbeddingModel = embedding.ModelFunc(nil) }, want: "elasticsearch: EmbeddingModel is required"},
		{name: "batcher", change: func(config *StoreConfig) { config.DocumentBatcher = (*configBatcher)(nil) }, want: "elasticsearch: DocumentBatcher is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := config
			test.change(&invalid)
			if err := invalid.Validate(); err == nil || err.Error() != test.want {
				t.Fatalf("Validate = %v, want %q", err, test.want)
			}
		})
	}
}

func TestToDocumentAcceptsIndexedNilMetadata(t *testing.T) {
	store := &Store{contentField: "content", metadataField: "metadata"}
	encoded, err := json.Marshal(map[string]any{"content": "hello", "metadata": metadata.Map(nil)})
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if decodeErr := json.Unmarshal(encoded, &source); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	document, err := store.toDocument(searchHit{ID: "doc-1", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if document.ID != "doc-1" || document.Text != "hello" || document.Metadata != nil {
		t.Fatalf("document = %+v", document)
	}
}

func TestToDocumentDecodesConfiguredMetadataObject(t *testing.T) {
	store := &Store{contentField: "content", embeddingField: "embedding", metadataField: "metadata"}
	document, err := store.toDocument(searchHit{
		ID: "doc-1",
		Source: map[string]any{
			"content":   "hello",
			"embedding": []any{0.1, 0.2},
			"metadata":  map[string]any{"tenant": "acme"},
		},
	})
	if err != nil {
		t.Fatalf("toDocument: %v", err)
	}
	values, err := document.Metadata.Values()
	if err != nil {
		t.Fatalf("metadata Values: %v", err)
	}
	if got := values["tenant"]; got != "acme" {
		t.Fatalf("tenant = %#v", got)
	}
}

func TestToDocumentRejectsMalformedConfiguredMetadata(t *testing.T) {
	store := &Store{contentField: "content", metadataField: "metadata"}
	_, err := store.toDocument(searchHit{
		ID:     "doc-1",
		Source: map[string]any{"content": "hello", "metadata": "not-an-object"},
	})
	if err == nil || !strings.Contains(err.Error(), `field "metadata" must be an object`) {
		t.Fatalf("toDocument error = %v", err)
	}
}
