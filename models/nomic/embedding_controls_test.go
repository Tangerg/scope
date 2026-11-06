package nomic_test

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
	"github.com/Tangerg/scope/models/nomic"
)

func TestEmbeddingNativeControlAdmission(t *testing.T) {
	for _, test := range []struct {
		field, value string
		valid        bool
	}{
		{"task_type", `"search_query"`, true}, {"task_type", `"search_document"`, true},
		{"task_type", `"classification"`, true}, {"task_type", `"clustering"`, true},
		{"task_type", `null`, true}, {"task_type", ``, true},
		{"task_type", `""`, false}, {"task_type", `"scope-invalid-enum"`, false},
		{"long_text_mode", `"mean"`, true}, {"long_text_mode", `"truncate"`, true},
		{"long_text_mode", `null`, true}, {"long_text_mode", ``, true},
		{"long_text_mode", `""`, false}, {"long_text_mode", `"scope-invalid-enum"`, false},
	} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s=%s/defaults=%t", test.field, test.value, defaults), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls.Add(1)
					var body map[string]json.RawMessage
					if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
						t.Error(err)
					}
					want := test.value
					if want == "null" {
						want = ""
					}
					if string(body[test.field]) != want {
						t.Errorf("wire %s=%s; want %s", test.field, body[test.field], want)
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(writer, `{"embeddings":[[1,2]]}`)
				}))
				defer server.Close()
				options := embedding.Options{Model: nomic.ModelEmbedTextV15}
				request := &embedding.Request{Texts: []string{"document"}}
				target := &request.Options
				if defaults {
					target = &options
				}
				if test.value != "" {
					if err := target.Extensions.Set(nomic.EmbeddingRequestExtensionKey, json.RawMessage(fmt.Sprintf(`{%q:%s}`, test.field, test.value))); err != nil {
						t.Fatal(err)
					}
				}
				model, err := nomic.NewEmbeddingModel(t.Context(), nomic.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if test.valid {
					if err != nil || calls.Load() != 1 {
						t.Fatalf("error=%v requests=%d", err, calls.Load())
					}
				} else if err == nil || !strings.Contains(err.Error(), test.field) || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
					t.Fatalf("invalid %s error=%v requests=%d", test.field, err, calls.Load())
				}
			})
		}
	}
}
