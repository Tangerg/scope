package ollama_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/ollama"
)

func TestEmbeddingPreservesNativeResultFacts(t *testing.T) {
	for _, test := range []struct {
		name, body     string
		invalid, usage bool
	}{
		{"null coordinate", `{"embeddings":[[null,1]]}`, true, false},
		{"explicit zero", `{"embeddings":[[0,1]]}`, false, false},
		{"zero usage", `{"embeddings":[[0,1]],"usage":{"prompt_tokens":0},"prompt_eval_count":0}`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, test.body)
			defer server.Close()
			model, err := ollama.NewEmbeddingModel(t.Context(), ollama.EmbeddingModelConfig{BaseURL: server.URL, DefaultOptions: embedding.Options{Model: "test"}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := embedding.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if test.invalid {
				if !errors.Is(err, embedding.ErrInvalidResponse) {
					t.Fatalf("response=%v error=%v; want invalid response", response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Outputs) != 1 || len(response.Outputs[0].Embedding) != 2 || response.Outputs[0].Embedding[0] != 0 || response.Outputs[0].Embedding[1] != 1 {
				t.Fatalf("outputs=%v", response.Outputs)
			}
			if (response.Metadata.Usage != nil) != test.usage {
				t.Fatalf("usage=%v; reported=%t", response.Metadata.Usage, test.usage)
			}
			if test.usage && response.Metadata.Usage.InputTokens != 0 {
				t.Fatalf("usage=%v", response.Metadata.Usage)
			}
		})
	}
}
