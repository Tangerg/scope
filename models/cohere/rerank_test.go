package cohere_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/cohere"
)

const cohereRerankResponseJSON = `{
  "id": "rerank-id",
  "results": [
    {"index": 0, "relevance_score": 0.98},
    {"index": 2, "relevance_score": 0.45}
  ],
  "meta": {"billed_units": {"search_units": 1}}
}`

func TestRerankModel(t *testing.T) {
	modeltest.RunRerankContract(t, modeltest.RerankContract{
		ModelID:      cohere.ModelRerankV35,
		Response:     cohereRerankResponseJSON,
		ExpectedPath: "/v2/rerank",
		LimitField:   "top_n",
		ResultsField: "results",
		Build: func(t *testing.T, baseURL string, defaults rerank.Options) rerank.Model {
			t.Helper()
			model, err := cohere.NewRerankModel(t.Context(), cohere.RerankModelConfig{
				APIKey:         "test-key",
				DefaultOptions: defaults,
				BaseURL:        baseURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			return model
		},
	})
}
