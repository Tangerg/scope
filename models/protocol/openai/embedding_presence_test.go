package openai_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
)

func TestEmbeddingRequiresNativeIndexAndCoordinates(t *testing.T) {
	for _, result := range []string{`{"embedding":[0,1]}`, `{"index":null,"embedding":[0,1]}`, `{"index":0,"embedding":[null,1]}`, `{"index":0,"embedding":[0,1]}`} {
		t.Run(result, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, `{"data":[`+result+`]}`)
			defer server.Close()
			model := newEmbeddingModel(t, server.URL, "text-embedding-3-small")
			request, err := embedding.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if result != `{"index":0,"embedding":[0,1]}` {
				if !errors.Is(err, embedding.ErrInvalidResponse) {
					t.Fatalf("response=%v error=%v", response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.Outputs[0].Embedding[0] != 0 {
				t.Fatal(response.Outputs[0].Embedding)
			}
		})
	}
}
