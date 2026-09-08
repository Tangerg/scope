//go:build integration

package voyage_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/voyage"
)

func TestRerankModel_Integration(t *testing.T) {
	modeltest.RunIntegrationRerank(t, modeltest.IntegrationRerankProbe{
		Provider: "voyage",
		Build: func(t *testing.T, key string) rerank.Model {
			t.Helper()
			model, err := voyage.NewRerankModel(t.Context(), voyage.RerankModelConfig{
				APIKey: key, DefaultOptions: rerank.Options{Model: voyage.ModelRerank25},
			})
			if err != nil {
				t.Fatal(err)
			}
			return model
		},
	})
}
