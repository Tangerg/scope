package jina_test

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/jina"
)

const jinaResponseJSON = `{
  "object": "list",
  "model": "jina-embeddings-v3",
  "data": [
    {"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]},
    {"object":"embedding","index":1,"embedding":[0.4,0.5,0.6]}
  ],
  "usage": {"prompt_tokens": 6, "total_tokens": 6}
}`

func TestEmbeddingModel(t *testing.T) {
	modeltest.RunEmbeddingContract(t, modeltest.EmbeddingContract{
		ModelID:      jina.ModelEmbeddingsV3,
		Response:     jinaResponseJSON,
		ExpectedPath: "/embeddings",
		Build: func(t *testing.T, baseURL string) embedding.Model {
			t.Helper()
			opts := embedding.Options{Model: jina.ModelEmbeddingsV3}
			err := opts.Validate()
			if err != nil {
				t.Fatalf("NewOptions: %v", err)
			}
			m, err := jina.NewEmbeddingModel(t.Context(), jina.EmbeddingModelConfig{
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
