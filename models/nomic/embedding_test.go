package nomic_test

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/nomic"
)

const nomicResponseJSON = `{
  "embeddings": [
    [0.1, 0.2, 0.3],
    [0.4, 0.5, 0.6]
  ],
  "model": "nomic-embed-text-v1.5",
  "usage": {"prompt_tokens": 6, "total_tokens": 6}
}`

func TestEmbeddingModel(t *testing.T) {
	modeltest.RunEmbeddingContract(t, modeltest.EmbeddingContract{
		ModelID:      nomic.ModelEmbedTextV15,
		Response:     nomicResponseJSON,
		ExpectedPath: "/embedding/text",
		Build: func(t *testing.T, baseURL string) embedding.Model {
			t.Helper()
			opts := embedding.Options{Model: nomic.ModelEmbedTextV15}
			err := opts.Validate()
			if err != nil {
				t.Fatalf("NewOptions: %v", err)
			}
			m, err := nomic.NewEmbeddingModel(t.Context(), nomic.EmbeddingModelConfig{
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
