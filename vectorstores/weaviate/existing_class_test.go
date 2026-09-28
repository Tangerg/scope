package weaviate

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/embedding"
)

// Existence is not agreement. Search converts Weaviate's distance into a Score
// with the metric from this store's own config, so a class that ranks by a
// different distance returns scores that are wrong rather than missing —
// nothing fails, the ranking is silently mis-scaled. initialize used to return
// as soon as the class existed, which accepted exactly that.
func TestCompareClassDistance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured DistanceMetric
		config     any
		wantErr    bool
	}{
		{
			name:       "agrees",
			configured: DistanceCosine,
			config:     map[string]any{"distance": "cosine"},
		},
		{
			name:       "disagrees",
			configured: DistanceCosine,
			config:     map[string]any{"distance": "l2-squared"},
			wantErr:    true,
		},
		{
			// Weaviate omits the key when the class uses its default, so the
			// omission has to read as cosine rather than as a refusal.
			name:       "omitted key is the Weaviate default",
			configured: DistanceCosine,
			config:     map[string]any{},
		},
		{
			name:       "omitted key still disagrees with a non-default",
			configured: DistanceL2Squared,
			config:     map[string]any{},
			wantErr:    true,
		},
		{
			name:       "config is not an object",
			configured: DistanceCosine,
			config:     "cosine",
			wantErr:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &Store{className: "Document", distanceMetric: test.configured}
			err := store.compareClassDistance(&models.Class{
				Class:             "Document",
				VectorIndexConfig: test.config,
			})
			if test.wantErr {
				if err == nil {
					t.Fatal("compareClassDistance() = nil error, want an incompatibility error")
				}
				if !errors.Is(err, ErrIncompatibleClass) {
					t.Fatalf("compareClassDistance() = %v, want %v", err, ErrIncompatibleClass)
				}
				if !strings.Contains(err.Error(), "Document") {
					t.Fatalf("compareClassDistance() = %v, want the class named", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("compareClassDistance() = %v, want nil", err)
			}
		})
	}
}

func TestNewStoreValidatesTheActualClassSchema(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		name := "existing"
		if fresh {
			name = "created"
		}
		t.Run(name, func(t *testing.T) {
			for _, sample := range []struct {
				name   string
				mutate func(*models.Class)
			}{
				{name: "compatible"},
				{name: "missing content", mutate: func(class *models.Class) { class.Properties = class.Properties[1:] }},
				{name: "missing metadata", mutate: func(class *models.Class) { class.Properties = class.Properties[:1] }},
				{name: "wrong metadata type", mutate: func(class *models.Class) { class.Properties[1].DataType = []string{"object"} }},
				{name: "content tokenizer", mutate: func(class *models.Class) { class.Properties[0].Tokenization = "field" }},
				{name: "content not searchable", mutate: func(class *models.Class) { value := false; class.Properties[0].IndexSearchable = &value }},
				{name: "distance mismatch", mutate: func(class *models.Class) { class.VectorIndexConfig = map[string]any{"distance": "dot"} }},
				{name: "invalid distance", mutate: func(class *models.Class) { class.VectorIndexConfig = map[string]any{"distance": false} }},
			} {
				t.Run(sample.name, func(t *testing.T) {
					class := &models.Class{Class: "Documents", VectorIndexConfig: map[string]any{"distance": "cosine"}, Properties: []*models.Property{
						{Name: "content", DataType: []string{"text"}, Tokenization: "word"},
						{Name: "metadata", DataType: []string{"text"}},
					}}
					if sample.mutate != nil {
						sample.mutate(class)
					}
					var mu sync.Mutex
					exists := !fresh
					server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						mu.Lock()
						defer mu.Unlock()
						writer.Header().Set("Content-Type", "application/json")
						switch {
						case request.URL.Path == "/v1/meta":
							_, _ = writer.Write([]byte(`{"version":"1.39.3"}`))
						case request.Method == http.MethodPost && request.URL.Path == "/v1/schema":
							exists = true
							_, _ = writer.Write([]byte(`{}`))
						case request.Method == http.MethodGet && request.URL.Path == "/v1/schema/Documents":
							if !exists {
								writer.WriteHeader(http.StatusNotFound)
								_, _ = writer.Write([]byte(`{}`))
								return
							}
							if err := jsonv2.MarshalWrite(writer, class); err != nil {
								t.Error(err)
							}
						default:
							t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
							http.NotFound(writer, request)
						}
					}))
					t.Cleanup(server.Close)
					client, err := weaviateclient.NewClient(weaviateclient.Config{Host: strings.TrimPrefix(server.URL, "http://"), Scheme: "http"})
					if err != nil {
						t.Fatal(err)
					}
					_, err = NewStore(t.Context(), StoreConfig{
						Client: client, ClassName: "Documents", InitializeSchema: fresh, DocumentBatcher: testBatcher{},
						EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
							t.Error("schema construction invoked embeddings")
							return nil, errors.New("unexpected embedding")
						}),
					})
					if sample.mutate == nil && err != nil {
						t.Fatal(err)
					}
					if sample.mutate != nil && !errors.Is(err, ErrIncompatibleClass) {
						t.Fatalf("NewStore error = %v, want ErrIncompatibleClass", err)
					}
				})
			}
		})
	}
}
