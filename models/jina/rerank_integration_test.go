//go:build integration

package jina_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/jina"
)

func TestRerankModel_Integration(t *testing.T) {
	modeltest.RunIntegrationRerank(t, modeltest.IntegrationRerankProbe{
		Provider: "jina",
		Build: func(t *testing.T, key string) rerank.Model {
			t.Helper()
			model, err := jina.NewRerankModel(t.Context(), jina.RerankModelConfig{
				APIKey: key, DefaultOptions: rerank.Options{Model: jina.ModelRerankerV3},
			})
			if err != nil {
				t.Fatal(err)
			}
			return model
		},
	})
}
