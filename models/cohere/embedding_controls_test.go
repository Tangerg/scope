package cohere_test

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
	"github.com/Tangerg/scope/models/cohere"
)

func TestEmbeddingNativeEnumAdmission(t *testing.T) {
	for _, test := range []struct {
		inputType    string
		truncate     *string
		invalidField string
	}{
		{inputType: "search_document"},
		{inputType: "search_query", truncate: new("NONE")},
		{inputType: "classification", truncate: new("START")},
		{inputType: "clustering", truncate: new("END")},
		{inputType: "image"},
		{inputType: "scope-invalid-enum", invalidField: "input_type"},
		{inputType: "", invalidField: "input_type"},
		{inputType: "search_document", truncate: new("scope-invalid-enum"), invalidField: "truncate"},
		{inputType: "search_document", truncate: new(""), invalidField: "truncate"},
	} {
		truncateName := "absent"
		if test.truncate != nil {
			truncateName = *test.truncate
		}
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("input=%s/truncate=%s/defaults=%t", test.inputType, truncateName, defaults), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls.Add(1)
					var body struct {
						InputType string  `json:"input_type"`
						Truncate  *string `json:"truncate"`
					}
					if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
						t.Error(err)
					}
					if body.InputType != test.inputType || (body.Truncate == nil) != (test.truncate == nil) || body.Truncate != nil && *body.Truncate != *test.truncate {
						t.Errorf("wire input_type=%q truncate=%v", body.InputType, body.Truncate)
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(writer, `{"embeddings":{"float":[[1,2]]}}`)
				}))
				defer server.Close()
				options := embedding.Options{Model: "embed-v4.0"}
				request := &embedding.Request{Texts: []string{"document"}}
				target := &request.Options
				if defaults {
					target = &options
				}
				if err := target.Extensions.Set(cohere.EmbeddingRequestExtensionKey, cohere.EmbeddingRequestOptions{InputType: test.inputType, Truncate: test.truncate}); err != nil {
					t.Fatal(err)
				}
				model, err := cohere.NewEmbeddingModel(t.Context(), cohere.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if test.invalidField == "" {
					if err != nil || calls.Load() != 1 {
						t.Fatalf("error=%v requests=%d", err, calls.Load())
					}
				} else if err == nil || !strings.Contains(err.Error(), test.invalidField) || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
					t.Fatalf("invalid enum error=%v requests=%d", err, calls.Load())
				}
			})
		}
	}
}

func TestEmbeddingAdmitsOnlyResolvedControls(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var body struct {
			InputType string  `json:"input_type"`
			Truncate  *string `json:"truncate"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
		}
		if body.InputType != "search_query" || body.Truncate != nil {
			t.Errorf("wire controls=%#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"embeddings":{"float":[[1,2]]}}`)
	}))
	defer server.Close()
	options := embedding.Options{Model: "embed-v4.0"}
	if err := options.Extensions.Set(cohere.EmbeddingRequestExtensionKey, json.RawMessage(`{"input_type":"scope-invalid-enum","truncate":"invalid"}`)); err != nil {
		t.Fatal(err)
	}
	model, err := cohere.NewEmbeddingModel(t.Context(), cohere.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	request := &embedding.Request{Texts: []string{"query"}}
	if err := request.Options.Extensions.Set(cohere.EmbeddingRequestExtensionKey, json.RawMessage(`{"input_type":"search_query","truncate":null}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err != nil || calls.Load() != 1 {
		t.Fatalf("error=%v requests=%d", err, calls.Load())
	}
}
