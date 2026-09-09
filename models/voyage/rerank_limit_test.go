package voyage_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/voyage"
)

// Voyage states the rerank ceiling as a limit rather than advice: "the number
// of documents cannot exceed 1,000". Refusing locally beats a provider round
// trip certain to fail.
//
// Cohere's equivalent is deliberately not enforced: it reads "we recommend
// against sending more than 1,000 documents in a single request", and refusing
// a recommendation would reject work Cohere would have done.
func TestRerankRefusesMoreDocumentsThanTheDocumentedCeiling(t *testing.T) {
	t.Parallel()

	model, err := voyage.NewRerankModel(t.Context(), voyage.RerankModelConfig{
		APIKey:         "test-key",
		BaseURL:        "http://127.0.0.1:1",
		DefaultOptions: rerank.Options{Model: "rerank-2.5"},
	})
	if err != nil {
		t.Fatalf("NewRerankModel: %v", err)
	}

	documents := make([]string, voyage.MaxDocumentsPerRerankRequest+1)
	for index := range documents {
		documents[index] = "document"
	}
	request, err := rerank.NewRequest("query", documents)
	if err != nil {
		t.Fatal(err)
	}

	// The base URL points nowhere, so a transport error would mean the ceiling
	// was not enforced before the request left.
	_, err = model.Call(t.Context(), request)
	if err == nil {
		t.Fatal("Call() = nil error, want the oversized batch refused")
	}
	if !strings.Contains(err.Error(), "at most") {
		t.Fatalf("Call() = %v, want the documented ceiling named", err)
	}
}
