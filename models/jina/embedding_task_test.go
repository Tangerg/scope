package jina_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/jina"
)

func TestEmbeddingAdmitsTaskForEffectiveModel(t *testing.T) {
	for _, test := range []struct {
		model string
		task  string
		valid bool
	}{
		{"jina-embeddings-v3", `"retrieval.query"`, true},
		{"jina-embeddings-v3", `"retrieval.passage"`, true},
		{"jina-embeddings-v3", `"text-matching"`, true},
		{"jina-embeddings-v3", `"classification"`, true},
		{"jina-embeddings-v3", `"separation"`, true},
		{"jina-embeddings-v3", `null`, true},
		{"jina-embeddings-v3", ``, true},
		{"jina-embeddings-v3", `""`, false},
		{"jina-embeddings-v3", `"scope-invalid-enum"`, false},
		{"jina-embeddings-v3", `"clustering"`, false},
		{"jina-embeddings-v3", `"code.query"`, false},
		{"jina-embeddings-v4", `"retrieval.query"`, true},
		{"jina-embeddings-v4", `"retrieval.passage"`, true},
		{"jina-embeddings-v4", `"text-matching"`, true},
		{"jina-embeddings-v4", `"code.query"`, true},
		{"jina-embeddings-v4", `"code.passage"`, true},
		{"jina-embeddings-v4", `"classification"`, false},
		{"jina-embeddings-v5-text-nano", `"retrieval.query"`, true},
		{"jina-embeddings-v5-text-small", `"retrieval.passage"`, true},
		{"jina-embeddings-v5-omni-nano", `"text-matching"`, true},
		{"jina-embeddings-v5-omni-small", `"classification"`, true},
		{"jina-embeddings-v5-text-small", `"clustering"`, true},
		{"jina-embeddings-v5-text-small", `"separation"`, false},
		{"jina-clip-v2", `"retrieval.query"`, true},
		{"jina-clip-v2", `"retrieval.passage"`, false},
		{"jina-code-embeddings-0.5b", `"nl2code.query"`, true},
		{"jina-code-embeddings-1.5b", `"code2completion.passage"`, true},
		{"jina-code-embeddings-1.5b", `"retrieval.query"`, false},
		{"jina-embeddings-v2-base-en", ``, true},
		{"custom-model", ``, true},
		{"custom-model", `"scope-invalid-enum"`, false},
	} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/task=%s/defaults=%t", test.model, test.task, defaults), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls.Add(1)
					var body struct {
						Model string          `json:"model"`
						Task  json.RawMessage `json:"task"`
					}
					if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
						t.Error(err)
					}
					wantTask := test.task
					if wantTask == "null" {
						wantTask = ""
					}
					if body.Model != test.model || string(body.Task) != wantTask {
						t.Errorf("wire model=%q task=%s; want %q %s", body.Model, body.Task, test.model, wantTask)
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(writer, `{"data":[{"index":0,"embedding":[1,2]}]}`)
				}))
				defer server.Close()
				options := embedding.Options{Model: jina.ModelEmbeddingsV3}
				request := &embedding.Request{Texts: []string{"document"}, Options: embedding.Options{Model: test.model}}
				target := &request.Options
				if defaults {
					target = &options
				}
				if test.task != "" {
					if err := target.Extensions.Set(jina.EmbeddingRequestExtensionKey, json.RawMessage(`{"task":`+test.task+`}`)); err != nil {
						t.Fatal(err)
					}
				}
				model, err := jina.NewEmbeddingModel(t.Context(), jina.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if test.valid {
					if err != nil || calls.Load() != 1 {
						t.Fatalf("error=%v requests=%d", err, calls.Load())
					}
				} else if err == nil || !strings.Contains(err.Error(), "task") || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
					t.Fatalf("invalid task error=%v requests=%d", err, calls.Load())
				}
			})
		}
	}
}
