package jina_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/jina"
)

const jinaRerankResponseJSON = `{
  "model": "jina-reranker-v3",
  "usage": {"total_tokens": 21},
  "results": [
    {"index": 0, "relevance_score": 0.97},
    {"index": 2, "relevance_score": 0.41}
  ]
}`

func TestRerankModel(t *testing.T) {
	modeltest.RunRerankContract(t, modeltest.RerankContract{
		ModelID:      jina.ModelRerankerV3,
		Response:     jinaRerankResponseJSON,
		ExpectedPath: "/rerank",
		Build: func(t *testing.T, baseURL string) rerank.Model {
			t.Helper()
			model, err := jina.NewRerankModel(t.Context(), jina.RerankModelConfig{
				APIKey:         "test-key",
				DefaultOptions: rerank.Options{Model: jina.ModelRerankerV3},
				BaseURL:        baseURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			return model
		},
	})
}
