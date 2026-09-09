package voyage_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/voyage"
)

// Voyage documents the ceiling exactly: "the maximum length of the list is
// 1,000".
//
// Refusing locally beats a provider round trip certain to fail, and this limit
// cannot be split away: one Call would become several, each with its own
// partial failure and usage accounting.
func TestEmbedRefusesMoreTextsThanTheDocumentedCeiling(t *testing.T) {
	t.Parallel()

	model, err := voyage.NewEmbeddingModel(t.Context(), voyage.EmbeddingModelConfig{
		APIKey:         "test-key",
		BaseURL:        "http://127.0.0.1:1",
		DefaultOptions: embedding.Options{Model: "voyage-3.5"},
	})
	if err != nil {
		t.Fatalf("NewEmbeddingModel: %v", err)
	}

	texts := make([]string, voyage.MaxTextsPerEmbedRequest+1)
	for index := range texts {
		texts[index] = "text"
	}
	request, err := embedding.NewRequest(texts)
	if err != nil {
		t.Fatal(err)
	}

	// The base URL points nowhere, so an error mentioning the transport would
	// mean the ceiling was not enforced before the request left.
	_, err = model.Call(t.Context(), request)
	if err == nil {
		t.Fatal("Call() = nil error, want the oversized batch refused")
	}
	if !strings.Contains(err.Error(), "at most") {
		t.Fatalf("Call() = %v, want the documented ceiling named", err)
	}
}
