package azureaisearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type writeTestBatcher struct{ single bool }

func (w writeTestBatcher) Batch(ctx context.Context, documents []*document.Document) ([][]*document.Document, error) {
	if !w.single {
		return [][]*document.Document{documents}, nil
	}
	batches := make([][]*document.Document, len(documents))
	for index, item := range documents {
		batches[index] = []*document.Document{item}
	}
	return batches, nil
}

// agreeingIndexBody is the index definition NewStore now reads to confirm the
// configured metric, written so the test server can answer the construction
// GET without every write test having to know about it.
func agreeingIndexBody(metric SimilarityMetric) string {
	return fmt.Sprintf(`{
		"fields": [{"name": %q, "vectorSearchProfile": "default-profile"}],
		"vectorSearch": {
			"profiles": [{"name": "default-profile", "algorithm": "default-hnsw"}],
			"algorithms": [{"name": "default-hnsw", "kind": "hnsw", "hnswParameters": {"metric": %q}}]
		}
	}`, DefaultEmbeddingField, metric)
}

func newWriteTestStore(t *testing.T, handler http.HandlerFunc, batcher writeTestBatcher) *Store {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			fmt.Fprint(writer, agreeingIndexBody(SimilarityCosine))
			return
		}
		handler(writer, request)
	}))
	t.Cleanup(server.Close)
	store, err := NewStore(t.Context(), StoreConfig{
		Endpoint: server.URL, APIKey: "test", IndexName: "documents", HTTPClient: server.Client(),
		SimilarityMetric: SimilarityCosine, DocumentBatcher: batcher,
		EmbeddingModel: embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
			outputs := make([]*embedding.Output, len(request.Texts))
			for index := range outputs {
				outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
			}
			return embedding.NewResponse(outputs, nil)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestIndexRejectsMetadataOverridingWriteFieldsBeforeAnyBatch(t *testing.T) {
	for _, field := range []string{"@search.action", DefaultIDField, DefaultContentField, DefaultEmbeddingField} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			store := newWriteTestStore(t, func(writer http.ResponseWriter, request *http.Request) {
				calls++
				fmt.Fprint(writer, `{"value":[{"key":"one","status":true,"statusCode":201}]}`)
			}, writeTestBatcher{single: true})
			second := &document.Document{ID: "two", Text: "second"}
			if err := second.Metadata.Set(field, "delete"); err != nil {
				t.Fatal(err)
			}
			request, err := vectorstore.NewIndexRequest([]*document.Document{{ID: "one", Text: "first"}, second})
			if err != nil {
				t.Fatal(err)
			}
			indexErr := store.Index(t.Context(), request)
			if indexErr == nil || !strings.Contains(indexErr.Error(), field) || calls != 0 {
				t.Fatalf("Index = %v, HTTP calls=%d; want field error before any HTTP call", indexErr, calls)
			}
		})
	}
}

func TestWritesRequireEveryDocumentAcknowledgment(t *testing.T) {
	for _, operation := range []string{"index", "delete"} {
		for _, test := range []struct {
			name      string
			body      string
			wantError bool
		}{
			{"success out of order", `{"value":[{"key":"two","status":true,"statusCode":200},{"key":"one","status":true,"statusCode":201}]}`, false},
			{"partial failure", `{"value":[{"key":"one","status":true,"statusCode":201},{"key":"two","status":false,"statusCode":503,"errorMessage":"busy"}]}`, true},
			{"missing result", `{"value":[{"key":"one","status":true,"statusCode":201}]}`, true},
			{"unknown key", `{"value":[{"key":"one","status":true,"statusCode":201},{"key":"other","status":true,"statusCode":201}]}`, true},
			{"duplicate key", `{"value":[{"key":"one","status":true,"statusCode":201},{"key":"one","status":true,"statusCode":201}]}`, true},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				store := newWriteTestStore(t, func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					if strings.HasSuffix(request.URL.Path, "/search") {
						fmt.Fprint(writer, `{"value":[{"id":"one"},{"id":"two"}]}`)
						return
					}
					if test.name == "partial failure" {
						writer.WriteHeader(http.StatusMultiStatus)
					}
					fmt.Fprint(writer, test.body)
				}, writeTestBatcher{})
				var writeErr error
				if operation == "index" {
					writeErr = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{
						{ID: "one", Text: "first"}, {ID: "two", Text: "second"},
					}})
				} else {
					writeErr = store.DeleteWhere(t.Context(), filter.EQ("category", "test"))
				}
				if (writeErr != nil) != test.wantError {
					t.Fatalf("write error = %v, want error %t", writeErr, test.wantError)
				}
				if test.name == "partial failure" && writeErr != nil && !strings.Contains(writeErr.Error(), "busy") {
					t.Fatalf("write error lost provider failure: %v", writeErr)
				}
			})
		}
	}
}

func TestIndexSplitsActionsAtServiceLimit(t *testing.T) {
	var batchSizes []int
	store := newWriteTestStore(t, func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Value []map[string]any `json:"value"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		batchSizes = append(batchSizes, len(body.Value))
		if len(body.Value) > maximumDocumentsPerBatch {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		results := make([]map[string]any, len(body.Value))
		for index, action := range body.Value {
			results[index] = map[string]any{"key": action["id"], "status": true, "statusCode": 201}
		}
		if err := json.NewEncoder(writer).Encode(map[string]any{"value": results}); err != nil {
			t.Error(err)
		}
	}, writeTestBatcher{})
	documents := make([]*document.Document, maximumDocumentsPerBatch+1)
	for index := range documents {
		documents[index] = &document.Document{ID: fmt.Sprint(index), Text: "text"}
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: documents}); err != nil {
		t.Fatal(err)
	}
	if len(batchSizes) != 2 || batchSizes[0] != maximumDocumentsPerBatch || batchSizes[1] != 1 {
		t.Fatalf("batch sizes = %v, want [%d 1]", batchSizes, maximumDocumentsPerBatch)
	}
}

func TestStoreConfigRejectsOverlappingWriteFields(t *testing.T) {
	for _, fields := range [][3]string{
		{"id", "id", "vector"}, {"id", "text", "id"}, {"id", "text", "text"},
		{"@search.action", "text", "vector"},
	} {
		t.Run(strings.Join(fields[:], "/"), func(t *testing.T) {
			config := StoreConfig{
				Endpoint: "https://example.search.windows.net", APIKey: "test", IndexName: "documents",
				IDField: fields[0], ContentField: fields[1], EmbeddingField: fields[2],
				SimilarityMetric: SimilarityCosine, DocumentBatcher: writeTestBatcher{},
				EmbeddingModel: embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
					return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
				}),
			}
			if err := config.Validate(); err == nil {
				t.Fatal("Validate accepted overlapping write fields")
			}
			if _, err := NewStore(t.Context(), config); err == nil {
				t.Fatal("NewStore accepted overlapping write fields")
			}
		})
	}
}
