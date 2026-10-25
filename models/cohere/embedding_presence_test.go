package cohere_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/cohere"
)

func TestEmbeddingRequiresReportedCoordinates(t *testing.T) {
	for _, test := range []struct {
		name, body   string
		valid, usage bool
	}{
		{"missing", `{}`, false, false}, {"null", `{"embeddings":null}`, false, false}, {"missing_float", `{"embeddings":{}}`, false, false}, {"null_coordinate", `{"embeddings":{"float":[[null,1]]}}`, false, false}, {"missing_usage", `{"embeddings":{"float":[[0,1]]},"meta":{"billed_units":{}}}`, true, false}, {"zero_usage", `{"embeddings":{"float":[[0,1]]},"meta":{"billed_units":{"input_tokens":0}}}`, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := modeltest.JSONServer(http.StatusOK, test.body, func(*http.Request) { calls++ })
			defer server.Close()
			var extensions metadata.Extensions
			if err := extensions.Set(cohere.EmbeddingRequestExtensionKey, cohere.EmbeddingRequestOptions{InputType: "search_document"}); err != nil {
				t.Fatal(err)
			}
			model, err := cohere.NewEmbeddingModel(t.Context(), cohere.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: embedding.Options{Model: "embed-v4.0", Extensions: extensions}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := embedding.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if calls != 1 {
				t.Fatalf("native response validation caused %d requests", calls)
			}
			if !test.valid {
				if !errors.Is(err, embedding.ErrInvalidResponse) {
					t.Fatalf("response=%v error=%v", response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (response.Metadata.Usage != nil) != test.usage {
				t.Fatalf("usage=%v", response.Metadata.Usage)
			}
			if response.Outputs[0].Embedding[0] != 0 {
				t.Fatal(response.Outputs[0].Embedding)
			}
		})
	}
}
