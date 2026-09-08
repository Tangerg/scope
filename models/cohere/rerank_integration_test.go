//go:build integration

package cohere_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/cohere"
)

func TestRerankModel_Integration(t *testing.T) {
	modeltest.RunIntegrationRerank(t, modeltest.IntegrationRerankProbe{
		Provider: "cohere",
		Build: func(t *testing.T, key string) rerank.Model {
			t.Helper()
			model, err := cohere.NewRerankModel(t.Context(), cohere.RerankModelConfig{
				APIKey: key, DefaultOptions: rerank.Options{Model: cohere.ModelRerankV35},
			})
			if err != nil {
				t.Fatal(err)
			}
			return model
		},
	})
}
