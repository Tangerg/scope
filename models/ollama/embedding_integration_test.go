//go:build integration

package ollama_test

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/ollama"
)

func TestEmbeddingModel_Integration(t *testing.T) {
	modeltest.RunIntegrationEmbedding(t, modeltest.IntegrationEmbeddingProbe{
		Provider: "ollama",
		Build: func(t *testing.T, _ string) embedding.Model {
			t.Helper()
			modelID, _ := modeltest.LookupEnv("SCOPE_TEST_OLLAMA_EMBEDDING_MODEL")
			if modelID == "" {
				modelID = "nomic-embed-text"
			}
			baseURL, _ := modeltest.LookupEnv("SCOPE_TEST_OLLAMA_BASE_URL")
			opts := embedding.Options{Model: modelID}
			err := opts.Validate()
			if err != nil {
				t.Fatal(err)
			}
			m, err := ollama.NewEmbeddingModel(t.Context(), ollama.EmbeddingModelConfig{
				DefaultOptions: opts,
				BaseURL:        baseURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			return m
		},
	})
}
