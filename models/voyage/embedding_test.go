package voyage_test

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/voyage"
)

const voyageResponseJSON = `{
  "object": "list",
  "data": [
    {"object":"embedding","embedding":[0.1,0.2,0.3],"index":0},
    {"object":"embedding","embedding":[0.4,0.5,0.6],"index":1}
  ],
  "model": "voyage-3-large",
  "usage": {"total_tokens": 6}
}`

func TestEmbeddingModel(t *testing.T) {
	modeltest.RunEmbeddingContract(t, modeltest.EmbeddingContract{
		ModelID:      "voyage-3-large",
		Response:     voyageResponseJSON,
		ExpectedPath: "/embeddings",
		Build: func(t *testing.T, baseURL string) embedding.Model {
			t.Helper()
			opts := embedding.Options{Model: "voyage-3-large"}
			err := opts.Validate()
			if err != nil {
				t.Fatalf("NewOptions: %v", err)
			}
			m, err := voyage.NewEmbeddingModel(t.Context(), voyage.EmbeddingModelConfig{
				APIKey:         "test-key",
				DefaultOptions: opts,
				BaseURL:        baseURL,
			})
			if err != nil {
				t.Fatalf("NewEmbeddingModel: %v", err)
			}
			return m
		},
	})
}
