package voyage_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/voyage"
)

const voyageRerankResponseJSON = `{
  "object": "list",
  "data": [
    {"index": 0, "relevance_score": 0.96},
    {"index": 2, "relevance_score": 0.37}
  ],
  "model": "rerank-2.5",
  "usage": {"total_tokens": 18}
}`

func TestRerankModel(t *testing.T) {
	modeltest.RunRerankContract(t, modeltest.RerankContract{
		ModelID:      voyage.ModelRerank25,
		Response:     voyageRerankResponseJSON,
		ExpectedPath: "/rerank",
		Build: func(t *testing.T, baseURL string) rerank.Model {
			t.Helper()
			model, err := voyage.NewRerankModel(t.Context(), voyage.RerankModelConfig{
				APIKey:         "test-key",
				DefaultOptions: rerank.Options{Model: voyage.ModelRerank25},
				BaseURL:        baseURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			return model
		},
	})
}
