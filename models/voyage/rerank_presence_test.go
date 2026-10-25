package voyage_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/voyage"
)

func TestRerankRequiresNativeIndexAndScore(t *testing.T) {
	for _, result := range []string{`{}`, `{"index":null,"relevance_score":0}`, `{"index":0,"relevance_score":null}`, `{"index":0,"relevance_score":0}`} {
		t.Run(result, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, `{"data":[`+result+`]}`)
			defer server.Close()
			model, err := voyage.NewRerankModel(t.Context(), voyage.RerankModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: rerank.Options{Model: "test"}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := rerank.NewRequest("query", []string{"document"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if result != `{"index":0,"relevance_score":0}` {
				if !errors.Is(err, rerank.ErrInvalidResponse) {
					t.Fatalf("response=%v error=%v", response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Index != 0 || response.Results[0].Score != 0 {
				t.Fatalf("results=%v", response.Results)
			}
			if response.Metadata.Usage != nil {
				t.Fatalf("invented usage=%v", response.Metadata.Usage)
			}
		})
	}
}
