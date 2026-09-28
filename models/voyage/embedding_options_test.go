package voyage_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/voyage"
)

func TestEmbeddingUsesEffectiveDimensions(t *testing.T) {
	for _, test := range []struct {
		name          string
		override      *int64
		vector        string
		wantDimension int64
		wantError     bool
	}{
		{"default", nil, "[1,2]", 2, false},
		{"default mismatch", nil, "[1,2,3]", 2, true},
		{"override", new(int64(3)), "[1,2,3]", 3, false},
		{"override mismatch", new(int64(3)), "[1,2]", 3, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
					t.Error(err)
				}
				if body["output_dimension"] != float64(test.wantDimension) {
					t.Errorf("wire dimensions = %v", body["output_dimension"])
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, strings.ReplaceAll(`{"data":[{"index":0,"embedding":VECTOR}]}`, "VECTOR", test.vector))
			}))
			defer server.Close()
			defaults := embedding.Options{Model: "test-model", Dimensions: new(int64(2))}

			model, err := voyage.NewEmbeddingModel(t.Context(), voyage.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: defaults})
			if err != nil {
				t.Fatal(err)
			}
			request := &embedding.Request{Texts: []string{"document"}, Options: embedding.Options{Dimensions: test.override}}
			response, err := model.Call(t.Context(), request)
			if (err != nil) != test.wantError {
				t.Fatalf("Call response=%v error=%v", response, err)
			}
			if test.wantError && response != nil {
				t.Fatal("invalid response escaped")
			}
			if request.Options.Dimensions != test.override || request.Options.Model != "" {
				t.Fatal("caller options mutated")
			}
		})
	}
}

func TestEmbeddingRejectsNativeCoreFieldsBeforeIO(t *testing.T) {
	for _, field := range []string{"model", "input", "output_dimension"} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/defaults=%t", field, defaults), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
				defer server.Close()
				options := embedding.Options{Model: "test-model"}
				request := &embedding.Request{Texts: []string{"document"}}
				target := &request.Options
				if defaults {
					target = &options
				}
				if err := target.Extensions.Set(voyage.EmbeddingRequestExtensionKey, map[string]any{field: nil, "input_type": "search_document"}); err != nil {
					t.Fatal(err)
				}
				model, err := voyage.NewEmbeddingModel(t.Context(), voyage.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if err == nil || !strings.Contains(err.Error(), "owned by Core") || calls != 0 {
					t.Fatalf("requests=%d error=%v", calls, err)
				}
			})
		}
	}
}
