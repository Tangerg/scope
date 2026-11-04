package google_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

func TestEmbeddingExtensionsRejectHTTPEnvelopeOverridesBeforeIO(t *testing.T) {
	for _, endpoint := range []struct {
		name, model, key, extension, response string
		vertex                                bool
	}{
		{
			name: "gemini batch", model: "gemini-embedding-001", key: google.EmbeddingRequestExtensionKey,
			extension: `{"httpOptions":{"extraBody":{"requests":[{"model":"models/other-model","content":{"parts":[{"text":"replacement"}]},"outputDimensionality":3}]}}}`,
			response:  `{"embeddings":[{"values":[1,2,3]}]}`,
		},
		{
			name: "vertex predict", model: "text-embedding-005", key: vertexai.EmbeddingRequestExtensionKey, vertex: true,
			extension: `{"httpOptions":{"extraBody":{"instances":[{"content":"replacement"}],"parameters":{"outputDimensionality":3}}}}`,
			response:  `{"predictions":[{"embeddings":{"values":[1,2,3]}}]}`,
		},
		{
			name: "vertex embed content", model: "gemini-embedding-2", key: vertexai.EmbeddingRequestExtensionKey, vertex: true,
			extension: `{"httpOptions":{"extraBody":{"content":{"parts":[{"text":"replacement"}]},"embedContentConfig":{"outputDimensionality":3}}}}`,
			response:  `{"embedding":{"values":[1,2,3]}}`,
		},
	} {
		for _, source := range []string{"request", "defaults"} {
			t.Run(endpoint.name+"/"+source, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(endpoint.response))
				}))
				t.Cleanup(server.Close)
				defaults := embedding.Options{Model: endpoint.model, Dimensions: new(int64(2))}
				request := &embedding.Request{Texts: []string{"canonical text"}}
				extensions := &request.Options.Extensions
				if source == "defaults" {
					extensions = &defaults.Extensions
				}
				if err := extensions.Set(endpoint.key, json.RawMessage(endpoint.extension)); err != nil {
					t.Fatal(err)
				}
				var model embedding.Model
				var err error
				if endpoint.vertex {
					model, err = vertexai.NewEmbeddingModel(t.Context(), vertexai.EmbeddingModelConfig{
						Client:         vertexai.ClientConfig{Project: "test-project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()},
						DefaultOptions: defaults,
					})
				} else {
					model, err = google.NewEmbeddingModel(t.Context(), google.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: defaults})
				}
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				failure, ok := errors.AsType[*invocation.Failure](err)
				if response != nil || !ok || failure.Kind != invocation.FailureNotExecuted || calls.Load() != 0 {
					t.Fatalf("response=%v error=%v calls=%d; want rejection before dispatch", response, err, calls.Load())
				}
			})
		}
	}
}
