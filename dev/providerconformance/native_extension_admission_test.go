package providerconformance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/cohere"
	"github.com/Tangerg/scope/models/jina"
	"github.com/Tangerg/scope/models/nomic"
	"github.com/Tangerg/scope/models/voyage"
)

func TestEmbeddingExtensionsRejectUnmappedControls(t *testing.T) {
	for _, test := range []struct {
		name, key, extension, response string
		build                          func(context.Context, string) (embedding.Model, error)
	}{
		{"cohere", cohere.EmbeddingRequestExtensionKey, `{"input_type":"search_document","truncate_input":false}`, `{"embeddings":{"float":[[1]]}}`, func(ctx context.Context, base string) (embedding.Model, error) {
			return cohere.NewEmbeddingModel(ctx, cohere.EmbeddingModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: embedding.Options{Model: "embed-v4.0"}})
		}},
		{"jina", jina.EmbeddingRequestExtensionKey, `{"truncation":false}`, `{"model":"test","data":[{"index":0,"embedding":[1]}]}`, func(ctx context.Context, base string) (embedding.Model, error) {
			return jina.NewEmbeddingModel(ctx, jina.EmbeddingModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: embedding.Options{Model: "test"}})
		}},
		{"nomic", nomic.EmbeddingRequestExtensionKey, `{"truncate":false}`, `{"model":"test","embeddings":[[1]]}`, func(ctx context.Context, base string) (embedding.Model, error) {
			return nomic.NewEmbeddingModel(ctx, nomic.EmbeddingModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: embedding.Options{Model: "test"}})
		}},
		{"voyage", voyage.EmbeddingRequestExtensionKey, `{"truncate":false}`, `{"model":"test","data":[{"index":0,"embedding":[1]}]}`, func(ctx context.Context, base string) (embedding.Model, error) {
			return voyage.NewEmbeddingModel(ctx, voyage.EmbeddingModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: embedding.Options{Model: "test"}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				fmt.Fprint(writer, test.response)
			}))
			t.Cleanup(server.Close)
			model, err := test.build(t.Context(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			request, _ := embedding.NewRequest([]string{"document"})
			if setErr := request.Options.Extensions.Set(test.key, json.RawMessage(test.extension)); setErr != nil {
				t.Fatal(setErr)
			}
			response, err := model.Call(t.Context(), request)
			if err == nil || response != nil || calls.Load() != 0 {
				t.Fatalf("unmapped control: response=%v error=%v provider_calls=%d", response, err, calls.Load())
			}
		})
	}
}

func TestRerankExtensionsRejectCompetingLimits(t *testing.T) {
	for _, test := range []struct {
		name, key, extension, response string
		build                          func(context.Context, string) (rerank.Model, error)
	}{
		{"cohere", cohere.RerankRequestExtensionKey, `{"top_n":1}`, `{"results":[{"index":0,"relevance_score":1}]}`, func(ctx context.Context, base string) (rerank.Model, error) {
			return cohere.NewRerankModel(ctx, cohere.RerankModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: rerank.Options{Model: "test"}})
		}},
		{"voyage", voyage.RerankRequestExtensionKey, `{"top_k":1}`, `{"model":"test","data":[{"index":0,"relevance_score":1}]}`, func(ctx context.Context, base string) (rerank.Model, error) {
			return voyage.NewRerankModel(ctx, voyage.RerankModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: rerank.Options{Model: "test"}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				fmt.Fprint(writer, test.response)
			}))
			t.Cleanup(server.Close)
			model, err := test.build(t.Context(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			request, _ := rerank.NewRequest("query", []string{"document"})
			if setErr := request.Options.Extensions.Set(test.key, json.RawMessage(test.extension)); setErr != nil {
				t.Fatal(setErr)
			}
			response, err := model.Call(t.Context(), request)
			if err == nil || response != nil || calls.Load() != 0 {
				t.Fatalf("competing limit: response=%v error=%v provider_calls=%d", response, err, calls.Load())
			}
		})
	}
}
