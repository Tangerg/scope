//go:build integration

package bedrock_test

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestEmbeddingModel_Integration(t *testing.T) {
	modeltest.RequireKey(t, "bedrock")
	region := modeltest.RequireEnv(t, "AWS_REGION")

	modelID, _ := modeltest.LookupEnv("SCOPE_TEST_BEDROCK_EMBEDDING_MODEL")
	if modelID == "" {
		modelID = "amazon.titan-embed-text-v2:0"
	}

	modeltest.RunIntegrationEmbedding(t, modeltest.IntegrationEmbeddingProbe{
		Provider: "bedrock",
		Build: func(t *testing.T, _ string) embedding.Model {
			t.Helper()
			opts := embedding.Options{Model: modelID}
			err := opts.Validate()
			if err != nil {
				t.Fatal(err)
			}
			m, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{
				DefaultOptions: opts,
				Region:         region,
			})
			if err != nil {
				t.Fatal(err)
			}
			return m
		},
	})
}
