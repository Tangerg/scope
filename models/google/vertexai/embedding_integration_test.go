//go:build integration

package vertexai_test

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/google/vertexai"
)

func TestEmbeddingModel_Integration(t *testing.T) {
	modeltest.RequireKey(t, "vertexai")
	project := modeltest.RequireEnv(t, "SCOPE_TEST_GCP_PROJECT")
	location := modeltest.RequireEnv(t, "SCOPE_TEST_GCP_LOCATION")

	modelID, _ := modeltest.LookupEnv("SCOPE_TEST_VERTEXAI_EMBEDDING_MODEL")
	if modelID == "" {
		modelID = "gemini-embedding-001"
	}

	modeltest.RunIntegrationEmbedding(t, modeltest.IntegrationEmbeddingProbe{
		Provider: "vertexai",
		Build: func(t *testing.T, _ string) embedding.Model {
			t.Helper()
			opts := embedding.Options{Model: modelID}
			err := opts.Validate()
			if err != nil {
				t.Fatal(err)
			}
			m, err := vertexai.NewEmbeddingModel(t.Context(), vertexai.EmbeddingModelConfig{
				Client: vertexai.ClientConfig{
					Project: project, Location: location,
				},
				DefaultOptions: opts,
			})
			if err != nil {
				t.Fatal(err)
			}
			return m
		},
	})
}
