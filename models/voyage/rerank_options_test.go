package voyage_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/voyage"
)

func TestRerankUsesEffectiveLimit(t *testing.T) {
	for _, test := range []struct {
		name      string
		override  *int
		wireLimit any
		results   int
		wantError bool
	}{
		{"inherit default", nil, float64(1), 1, false},
		{"explicit all", new(0), nil, 2, false},
		{"explicit override", new(2), float64(2), 2, false},
		{"default response mismatch", nil, float64(1), 2, true},
		{"all response mismatch", new(0), nil, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
					t.Error(err)
				}
				if body["top_k"] != test.wireLimit {
					t.Errorf("wire limit=%v, want %v", body["top_k"], test.wireLimit)
				}
				items := `{"index":0,"relevance_score":0.9}`
				if test.results == 2 {
					items += `,{"index":1,"relevance_score":0.4}`
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"data":[%s]}`, items)
			}))
			defer server.Close()
			model, err := voyage.NewRerankModel(t.Context(), voyage.RerankModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: rerank.Options{Model: "test", TopK: new(1)}})
			if err != nil {
				t.Fatal(err)
			}
			request := &rerank.Request{Query: "query", Documents: []string{"first", "second"}, Options: rerank.Options{TopK: test.override}}
			response, err := model.Call(t.Context(), request)
			if (err != nil) != test.wantError {
				t.Fatalf("response=%v error=%v", response, err)
			}
			if request.Options.TopK != test.override || request.Options.Model != "" {
				t.Fatal("caller options mutated")
			}
		})
	}
}
func TestRerankRejectsInvalidEffectiveLimitBeforeIO(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
	defer server.Close()
	model, err := voyage.NewRerankModel(t.Context(), voyage.RerankModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: rerank.Options{Model: "test", TopK: new(3)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Call(t.Context(), &rerank.Request{Query: "query", Documents: []string{"first", "second"}})
	if err == nil || calls != 0 {
		t.Fatalf("requests=%d error=%v", calls, err)
	}
}
