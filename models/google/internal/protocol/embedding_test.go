package protocol_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// genai embed response: { embeddings: [{ values: [...] }, ...] }
const googleEmbedJSON = `{
  "embeddings": [
    {"values": [0.1, 0.2, 0.3]},
    {"values": [0.4, 0.5, 0.6]}
  ]
}`

func TestEmbeddingModel_Call_Mock(t *testing.T) {
	srv := modeltest.JSONServer(http.StatusOK, googleEmbedJSON)
	t.Cleanup(srv.Close)

	opts := embedding.Options{Model: protocol.ModelGeminiEmbedding2}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := protocol.NewEmbeddingModel(t.Context(), protocol.EmbeddingModelConfig{
		Provider:       "google",
		Client:         protocol.ClientConfig{APIKey: "test-key", BaseURL: srv.URL},
		DefaultOptions: opts,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := embedding.NewRequest([]string{"foo", "bar"})
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(out.Outputs) != 2 {
		t.Fatalf("got %d outputs; want 2", len(out.Outputs))
	}
	hugeDimensions := int64(1 << 31)
	req.Options = embedding.Options{Dimensions: &hugeDimensions}
	if _, err := m.Call(t.Context(), req); err == nil {
		t.Fatal("Call accepted dimensions that overflow the provider wire type")
	}
}
