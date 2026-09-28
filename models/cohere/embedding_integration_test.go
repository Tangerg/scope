//go:build integration

package cohere_test

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/cohere"
)

func TestEmbeddingModel_Integration(t *testing.T) {
	modeltest.RunIntegrationEmbedding(t, modeltest.IntegrationEmbeddingProbe{
		Provider: "cohere",
		Build: func(t *testing.T, key string) embedding.Model {
			t.Helper()
			modelID, _ := modeltest.LookupEnv("SCOPE_TEST_COHERE_MODEL")
			if modelID == "" {
				modelID = "embed-v4.0"
			}
			opts := embedding.Options{Model: modelID}
			err := opts.Validate()
			if err != nil {
				t.Fatal(err)
			}
			if err := opts.Extensions.Set(cohere.EmbeddingRequestExtensionKey, cohere.EmbeddingRequestOptions{
				InputType: "search_document",
			}); err != nil {
				t.Fatal(err)
			}
			m, err := cohere.NewEmbeddingModel(t.Context(), cohere.EmbeddingModelConfig{
				APIKey:         key,
				DefaultOptions: opts,
			})
			if err != nil {
				t.Fatal(err)
			}
			return m
		},
	})
}
