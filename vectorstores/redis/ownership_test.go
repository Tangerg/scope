package redis

import (
	"context"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
)

type ownershipBatcher struct{}

func (ownershipBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func TestNewStoreOwnsMetadataFields(t *testing.T) {
	// Construction reads the index it is pointed at, so this answers from a
	// scripted client rather than whatever happens to listen on localhost.
	client := &searchIndexClient{
		names:      []string{DefaultIndexName},
		attributes: []goredis.FTAttribute{vectorAttribute("COSINE", 2)},
	}

	fields := []MetadataField{{Name: "tenant", Type: FieldTag}}
	store, err := NewStore(t.Context(), StoreConfig{
		Client:         client,
		MetadataFields: fields,
		EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
			return nil, nil
		}),
		DocumentBatcher: ownershipBatcher{},
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	fields[0].Name = "mutated"
	if got := store.metadataFields[0].Name; got != "tenant" {
		t.Fatalf("store retained caller-owned MetadataFields: got %q", got)
	}
	if got := store.fieldTypes["tenant"]; got != FieldTag {
		t.Fatalf("fieldTypes[tenant] = %q, want %q", got, FieldTag)
	}
}
