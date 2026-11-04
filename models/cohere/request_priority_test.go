package cohere_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/cohere"
)

func TestRequestPriorityAdmission(t *testing.T) {
	for _, capability := range []struct {
		name     string
		response string
		call     func(*testing.T, string, *int, bool) error
	}{
		{name: "embedding", response: `{"embeddings":{"float":[[1,2]]}}`, call: func(t *testing.T, endpoint string, priority *int, defaults bool) error {
			options := embedding.Options{Model: "embed-v4.0"}
			request := &embedding.Request{Texts: []string{"document"}}
			target := &request.Options
			if defaults {
				target = &options
			}
			if err := target.Extensions.Set(cohere.EmbeddingRequestExtensionKey, cohere.EmbeddingRequestOptions{InputType: "search_document", Priority: priority}); err != nil {
				t.Fatal(err)
			}
			model, err := cohere.NewEmbeddingModel(t.Context(), cohere.EmbeddingModelConfig{APIKey: "test", BaseURL: endpoint, DefaultOptions: options})
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Call(t.Context(), request)
			return err
		}},
		{name: "rerank", response: `{"results":[{"index":0,"relevance_score":0.9}]}`, call: func(t *testing.T, endpoint string, priority *int, defaults bool) error {
			options := rerank.Options{Model: cohere.ModelRerankV35}
			request := &rerank.Request{Query: "query", Documents: []string{"document"}}
			target := &request.Options
			if defaults {
				target = &options
			}
			if err := target.Extensions.Set(cohere.RerankRequestExtensionKey, cohere.RerankRequestOptions{Priority: priority}); err != nil {
				t.Fatal(err)
			}
			model, err := cohere.NewRerankModel(t.Context(), cohere.RerankModelConfig{APIKey: "test", BaseURL: endpoint, DefaultOptions: options})
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Call(t.Context(), request)
			return err
		}},
	} {
		for _, defaults := range []bool{false, true} {
			for _, test := range []struct {
				name     string
				priority *int
				valid    bool
			}{
				{name: "absent", valid: true},
				{name: "zero", priority: new(0), valid: true},
				{name: "maximum", priority: new(999), valid: true},
				{name: "negative", priority: new(-1)},
				{name: "above maximum", priority: new(1000)},
			} {
				t.Run(fmt.Sprintf("%s/defaults=%t/%s", capability.name, defaults, test.name), func(t *testing.T) {
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						var request struct {
							Priority *int `json:"priority"`
						}
						if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
							t.Error(err)
						}
						if (request.Priority == nil) != (test.priority == nil) || request.Priority != nil && *request.Priority != *test.priority {
							t.Errorf("wire priority = %v; want %v", request.Priority, test.priority)
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, capability.response)
					}))
					defer server.Close()
					err := capability.call(t, server.URL, test.priority, defaults)
					if test.valid {
						if err != nil || calls.Load() != 1 {
							t.Fatalf("Call error=%v requests=%d", err, calls.Load())
						}
					} else if err == nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
						t.Fatalf("invalid priority error=%v requests=%d", err, calls.Load())
					}
				})
			}
		}
	}
}
