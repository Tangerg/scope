package azureopenai_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/azureopenai"
)

const azureEmbedJSON = `{
  "object": "list",
  "model": "text-embedding-ada-002",
  "data": [
    {"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]},
    {"object":"embedding","index":1,"embedding":[0.4,0.5,0.6]}
  ],
  "usage": {"prompt_tokens": 6, "total_tokens": 6}
}`

func TestEmbeddingModel_Call_Mock(t *testing.T) {
	srv := modeltest.JSONServer(http.StatusOK, azureEmbedJSON)
	t.Cleanup(srv.Close)

	opts := embedding.Options{Model: "text-embedding-ada-002"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := azureopenai.NewEmbeddingModel(t.Context(), azureopenai.EmbeddingModelConfig{
		Config:         azureopenai.Config{APIKey: "test-key", BaseURL: srv.URL + "/openai/v1/"},
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
}
