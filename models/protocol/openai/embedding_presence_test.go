package openai_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestEmbeddingRequiresNativeIndexAndCoordinates(t *testing.T) {
	for _, result := range []string{`{"embedding":[0,1]}`, `{"index":null,"embedding":[0,1]}`, `{"index":0.5,"embedding":[0,1]}`, `{"index":"0","embedding":[0,1]}`, `{"index":0,"embedding":[null,1]}`, `{"index":0,"embedding":[0,1]}`} {
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

func TestNonVectorEncodingIsRejectedBeforeEmbedding(t *testing.T) {
	boundary := new(numericTransport)
	model, err := openai.NewEmbeddingModel(t.Context(), openai.EmbeddingModelConfig{Provider: "openai", APIKey: "test", BaseURL: "https://api.example", HTTPClient: &http.Client{Transport: boundary}, DefaultOptions: embedding.Options{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []any{"base64", 7, nil} {
		request := &embedding.Request{Texts: []string{"paid text"}}
		if err = request.Options.Extensions.Set(openai.EmbeddingRequestExtensionKey, map[string]any{"encoding_format": format}); err != nil {
			t.Fatal(err)
		}
		if response, callErr := model.Call(t.Context(), request); response != nil || !errors.Is(callErr, errors.ErrUnsupported) || boundary.calls != 0 {
			t.Fatalf("encoding %v: response %v, error %v, attempts %d", format, response, callErr, boundary.calls)
		}
	}
}
