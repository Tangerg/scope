package voyage_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/voyage"
)

const voyageRerankResponseJSON = `{
  "object": "list",
  "data": [
    {"index": 0, "relevance_score": 0.96},
    {"index": 2, "relevance_score": -0.37}
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

func TestRerankPreservesNativeNegativeScores(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, voyageRerankResponseJSON)
	t.Cleanup(server.Close)
	model, err := voyage.NewRerankModel(t.Context(), voyage.RerankModelConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: rerank.Options{Model: voyage.ModelRerank25},
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
	if len(response.Results) != 2 || response.Results[0].Index != 0 || response.Results[0].Score != 0.96 || response.Results[1].Index != 2 || response.Results[1].Score != -0.37 {
		t.Fatalf("native relevance was changed: %#v", response.Results)
	}
}
