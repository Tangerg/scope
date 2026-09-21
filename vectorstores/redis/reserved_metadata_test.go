package redis

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestIndexRejectsReservedMetadataBeforeBatching(t *testing.T) {
	for _, field := range []string{"content", "embedding", "metadata_json"} {
		t.Run(field, func(t *testing.T) {
			attributes, err := metadata.FromValues(map[string]any{field: "overwrite"})
			if err != nil {
				t.Fatal(err)
			}
			store := &Store{contentField: "content", embeddingField: "embedding", metadataJSONField: "metadata_json"}
			err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "first", Text: "safe"}, {ID: "second", Text: "safe", Metadata: attributes}}})
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("reserved metadata error = %v", err)
			}
		})
	}
}
