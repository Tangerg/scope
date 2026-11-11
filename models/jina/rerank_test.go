package jina_test

import (
	"net/http"
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
    {"index": 2, "relevance_score": -0.1}
  ]
}`

func TestRerankModel(t *testing.T) {
	modeltest.RunRerankContract(t, modeltest.RerankContract{
		ModelID:      jina.ModelRerankerV3,
		Response:     jinaRerankResponseJSON,
		ExpectedPath: "/rerank",
		LimitField:   "top_n",
		ResultsField: "results",
		Build: func(t *testing.T, baseURL string, defaults rerank.Options) rerank.Model {
			t.Helper()
			model, err := jina.NewRerankModel(t.Context(), jina.RerankModelConfig{
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

func TestRerankPreservesNativeNegativeScores(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, jinaRerankResponseJSON)
	t.Cleanup(server.Close)
	model, err := jina.NewRerankModel(t.Context(), jina.RerankModelConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: rerank.Options{Model: jina.ModelRerankerV3},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := rerank.NewRequest("capital of France", []string{"Paris", "Berlin", "Rome"})
	if err != nil {
		t.Fatal(err)
	}
	request.Options.TopK = new(2)
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 || response.Results[0].Index != 0 || response.Results[0].Score != 0.97 || response.Results[1].Index != 2 || response.Results[1].Score != -0.1 {
		t.Fatalf("native relevance was changed: %#v", response.Results)
	}
}
