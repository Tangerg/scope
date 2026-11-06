package voyage_test

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
	"github.com/Tangerg/scope/models/voyage"
)

func TestEmbeddingInputTypeAdmission(t *testing.T) {
	for _, test := range []struct {
		value string
		valid bool
	}{
		{`"query"`, true}, {`"document"`, true}, {`null`, true}, {``, true}, {`""`, false}, {`"scope-invalid-enum"`, false},
	} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("input_type=%s/defaults=%t", test.value, defaults), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls.Add(1)
					var body struct {
						InputType json.RawMessage `json:"input_type"`
					}
					if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
						t.Error(err)
					}
					want := test.value
					if want == "null" {
						want = ""
					}
					if string(body.InputType) != want {
						t.Errorf("wire input_type=%s; want %s", body.InputType, want)
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(writer, `{"data":[{"index":0,"embedding":[1,2]}]}`)
				}))
				defer server.Close()
				options := embedding.Options{Model: voyage.Model4}
				request := &embedding.Request{Texts: []string{"document"}}
				target := &request.Options
				if defaults {
					target = &options
				}
				if test.value != "" {
					if err := target.Extensions.Set(voyage.EmbeddingRequestExtensionKey, json.RawMessage(`{"input_type":`+test.value+`}`)); err != nil {
						t.Fatal(err)
					}
				}
				model, err := voyage.NewEmbeddingModel(t.Context(), voyage.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if test.valid {
					if err != nil || calls.Load() != 1 {
						t.Fatalf("error=%v requests=%d", err, calls.Load())
					}
				} else if err == nil || !strings.Contains(err.Error(), "input_type") || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
					t.Fatalf("invalid input_type error=%v requests=%d", err, calls.Load())
				}
			})
		}
	}
}
