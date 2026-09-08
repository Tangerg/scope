package azureaisearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestHybridSearchSendsLexicalAndVectorEvidence(t *testing.T) {
	t.Parallel()

	seen := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		seen <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"value":[{"id":"one","content":"Paris","@search.score":0.03}]}`))
	}))
	t.Cleanup(server.Close)

	embeddingClient, err := embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		output, err := embedding.NewOutput([]float64{1, 0}, nil)
		if err != nil {
			return nil, err
		}
		return embedding.NewResponse([]*embedding.Output{output}, nil)
	}))
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{
		endpoint: server.URL, apiKey: "key", indexName: "documents", apiVersion: DefaultAPIVersion,
		idField: DefaultIDField, contentField: DefaultContentField, embeddingField: DefaultEmbeddingField,
		embeddingClient: embeddingClient, similarityMetric: SimilarityCosine, httpClient: server.Client(),
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{
		Query: "capital of France", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, TopK: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("Search() = %#v", response)
	}

	body := <-seen
	if body["search"] != "capital of France" || body["searchFields"] != DefaultContentField {
		t.Fatalf("hybrid lexical request = %#v", body)
	}
	if vectorQueries, ok := body["vectorQueries"].([]any); !ok || len(vectorQueries) != 1 {
		t.Fatalf("hybrid vector request = %#v", body["vectorQueries"])
	}
}

func TestSearchPreservesMetadataAcrossServerPages(t *testing.T) {
	calls := 0
	store := newWriteTestStore(t, func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			fmt.Fprint(writer, `{"value":[{"id":"one","content":"first","@search.score":1,"large":9007199254740993,"nested":{"large":9223372036854775807}}],"@search.nextPageParameters":{"top":1,"skip":1,"vectorQueries":[{"kind":"vector","vector":[1,0],"fields":"contentVector","k":2}]}}`)
			return
		}
		var body struct {
			Skip int `json:"skip"`
			Top  int `json:"top"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Skip != 1 || body.Top != 1 {
			t.Errorf("continuation body = %+v", body)
		}
		fmt.Fprint(writer, `{"value":[{"id":"two","content":"second","@search.score":0.5}]}`)
	}, writeTestBatcher{})
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{
		Query: "text", Options: vectorstore.SearchOptions{TopK: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 || calls != 2 {
		t.Errorf("results=%d calls=%d, want 2, 2", len(response.Results), calls)
	}
	if len(response.Results) == 0 {
		t.Fatal("missing first result")
	}
	metadata := response.Results[0].Document.Metadata
	if string(metadata["large"]) != "9007199254740993" || string(metadata["nested"]) != `{"large":9223372036854775807}` {
		t.Errorf("metadata lost JSON precision: %s, %s", metadata["large"], metadata["nested"])
	}
}

func TestDeleteWhereConsumesServerPagesBeforeWriting(t *testing.T) {
	for _, failContinuation := range []bool{false, true} {
		t.Run(fmt.Sprintf("continuation_error=%t", failContinuation), func(t *testing.T) {
			queries, writes := 0, 0
			store := newWriteTestStore(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(request.URL.Path, "/search") {
					queries++
					if queries == 1 {
						fmt.Fprint(writer, `{"value":[{"id":"one"}],"@search.nextPageParameters":{"select":"id","filter":"category eq 'test'","top":999,"skip":1}}`)
					} else if failContinuation {
						writer.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(writer, `{"error":{"message":"query failed"}}`)
					} else {
						fmt.Fprint(writer, `{"value":[{"id":"two"}]}`)
					}
					return
				}
				writes++
				fmt.Fprint(writer, `{"value":[{"key":"one","status":true,"statusCode":200},{"key":"two","status":true,"statusCode":200}]}`)
			}, writeTestBatcher{})
			deleteErr := store.DeleteWhere(t.Context(), filter.EQ("category", "test"))
			if failContinuation {
				if deleteErr == nil || queries != 2 || writes != 0 {
					t.Fatalf("DeleteWhere = %v, queries=%d writes=%d; want error, 2, 0", deleteErr, queries, writes)
				}
			} else if deleteErr != nil || queries != 2 || writes != 1 {
				t.Fatalf("DeleteWhere = %v, queries=%d writes=%d; want nil, 2, 1", deleteErr, queries, writes)
			}
		})
	}
}

func TestDeleteWhereRejectsMissingDocumentIDs(t *testing.T) {
	store := newWriteTestStore(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"value":[{}]}`)
	}, writeTestBatcher{})
	if err := store.DeleteWhere(t.Context(), filter.EQ("category", "test")); err == nil {
		t.Fatal("DeleteWhere concealed a row without its document ID")
	}
}
