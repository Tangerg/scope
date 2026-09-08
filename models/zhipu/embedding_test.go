package zhipu_test

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/zhipu"
)

const zhipuEmbedResponseJSON = `{
  "object": "list",
  "model": "embedding-3",
  "data": [
    {"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]},
    {"object":"embedding","index":1,"embedding":[0.4,0.5,0.6]}
  ],
  "usage": {"prompt_tokens": 6, "total_tokens": 6}
}`

func TestEmbeddingModel(t *testing.T) {
	modeltest.RunEmbeddingContract(t, modeltest.EmbeddingContract{
		ModelID:  zhipu.ModelEmbedding3,
		Response: zhipuEmbedResponseJSON,
		Build: func(t *testing.T, baseURL string) embedding.Model {
			t.Helper()
			opts := embedding.Options{Model: zhipu.ModelEmbedding3}
			err := opts.Validate()
			if err != nil {
				t.Fatalf("NewOptions: %v", err)
			}
			m, err := zhipu.NewEmbeddingModel(t.Context(), zhipu.EmbeddingModelConfig{
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

func TestEmbeddingConfigRejectsIncompleteAndInvalidOptions(t *testing.T) {
	tests := []zhipu.EmbeddingModelConfig{
		{},
		{APIKey: "key"},
		{APIKey: "key", DefaultOptions: embedding.Options{Model: " model "}},
	}
	for _, config := range tests {
		if err := config.Validate(); err == nil {
			t.Fatalf("Validate(%#v) error = nil", config)
		}
	}
}
