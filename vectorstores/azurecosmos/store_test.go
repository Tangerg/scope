package azurecosmos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type testEmbeddingModel struct{}

func (t testEmbeddingModel) Call(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, len(request.Texts))
	for index := range outputs {
		outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
	}
	return embedding.NewResponse(outputs, nil)
}

type testBatcher struct{}

func (t testBatcher) Batch(ctx context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func newTestStore(t *testing.T, partitionKeyField string, handler http.HandlerFunc) *Store {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			fmt.Fprint(writer, `{"id":"test","readableLocations":[],"writableLocations":[]}`)
			return
		}
		handler(writer, request)
	}))
	t.Cleanup(server.Close)
	credential, err := azcosmos.NewKeyCredential("dGVzdA==")
	if err != nil {
		t.Fatal(err)
	}
	client, err := azcosmos.NewClientWithKey(server.URL, credential, nil)
	if err != nil {
		t.Fatal(err)
	}
	container, err := client.NewContainer("test", "vectors")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(StoreConfig{
		Container: container, EmbeddingModel: testEmbeddingModel{}, DocumentBatcher: testBatcher{},
		PartitionKey: "library", PartitionKeyField: partitionKeyField,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSearchScopesRankingToPartition(t *testing.T) {
	store := newTestStore(t, "", func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("x-ms-documentdb-partitionkey"); got != `["library"]` {
			t.Errorf("partition key = %q, want library", got)
			writer.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(writer, `{"code":"BadRequest","message":"cross-partition ranking is unsupported"}`)
			return
		}
		fmt.Fprint(writer, `{"Documents":[{"_id":"one","_content":"text","_metadata":{},"_vector_score":1}],"_count":1}`)
	})
	request, err := vectorstore.NewSearchRequest("text")
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "one" || response.Results[0].Score != 1 {
		t.Fatalf("Search = %#v", response)
	}
}

func TestIndexAndDeleteUseBoundPartition(t *testing.T) {
	for _, field := range []string{DefaultPartitionKeyField, "tenant"} {
		t.Run(field, func(t *testing.T) {
			var indexed, deleted []string
			pages := 0
			store := newTestStore(t, field, func(writer http.ResponseWriter, request *http.Request) {
				if got := request.Header.Get("x-ms-documentdb-partitionkey"); got != `["library"]` {
					t.Errorf("%s partition key = %q, want library", request.Method, got)
				}
				if request.Method == http.MethodDelete {
					if pages != 3 {
						t.Errorf("delete started before enumeration completed: pages=%d", pages)
					}
					deleted = append(deleted, strings.TrimPrefix(request.URL.Path, "/dbs/test/colls/vectors/docs/"))
					writer.WriteHeader(http.StatusNoContent)
					return
				}
				if request.Header.Get("Content-Type") == "application/query+json" {
					pages++
					switch pages {
					case 1:
						writer.Header().Set("x-ms-continuation", "empty-page")
						fmt.Fprint(writer, `{"Documents":[{"_id":"one"}],"_count":1}`)
					case 2:
						writer.Header().Set("x-ms-continuation", "last-page")
						fmt.Fprint(writer, `{"Documents":[],"_count":0}`)
					case 3:
						fmt.Fprint(writer, `{"Documents":[{"_id":"two"}],"_count":1}`)
					default:
						t.Errorf("unexpected query %d", pages)
						fmt.Fprint(writer, `{"Documents":[],"_count":0}`)
					}
					return
				}
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				if got := string(payload[field]); got != `"library"` {
					t.Errorf("stored partition field = %s, want library", got)
				}
				var id string
				if err := json.Unmarshal(payload["id"], &id); err != nil {
					t.Error(err)
				}
				indexed = append(indexed, id)
				fmt.Fprint(writer, `{}`)
			})
			request, err := vectorstore.NewIndexRequest([]*document.Document{
				{ID: "one", Text: "first"}, {ID: "two", Text: "second"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Index(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteWhere(context.Background(), filter.EQ("category", "test")); err != nil {
				t.Fatal(err)
			}
			want := []string{"one", "two"}
			if !slices.Equal(indexed, want) || !slices.Equal(deleted, want) {
				t.Fatalf("indexed=%v deleted=%v, want %v", indexed, deleted, want)
			}
		})
	}
}

func TestStoreConfigRejectsAmbiguousStorageFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*StoreConfig)
	}{
		{"missing partition", func(config *StoreConfig) { config.PartitionKey = "" }},
		{"partition is ID", func(config *StoreConfig) { config.PartitionKeyField = "id" }},
		{"partition is content", func(config *StoreConfig) { config.PartitionKeyField = DefaultContentField }},
		{"nested partition path", func(config *StoreConfig) { config.PartitionKeyField = "tenant/id" }},
		{"content is ID", func(config *StoreConfig) { config.ContentField = "id" }},
		{"content is metadata", func(config *StoreConfig) { config.ContentField = DefaultMetadataField }},
		{"embedding is metadata", func(config *StoreConfig) { config.EmbeddingField = DefaultMetadataField }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := StoreConfig{
				Container: new(azcosmos.ContainerClient), EmbeddingModel: testEmbeddingModel{},
				DocumentBatcher: testBatcher{}, PartitionKey: "library",
			}
			test.change(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("Validate accepted invalid storage configuration")
			}
			if _, err := NewStore(config); err == nil {
				t.Fatal("NewStore accepted invalid storage configuration")
			}
		})
	}
}

func ExampleNewStore() {
	credential, err := azcosmos.NewKeyCredential("dGVzdA==")
	if err != nil {
		panic(err)
	}
	client, err := azcosmos.NewClientWithKey("https://example.documents.azure.com", credential, nil)
	if err != nil {
		panic(err)
	}
	container, err := client.NewContainer("knowledge", "documents")
	if err != nil {
		panic(err)
	}
	// The host provisions this container with partition-key path /tenant.
	store, err := NewStore(StoreConfig{
		Container:         container,
		PartitionKeyField: "tenant",
		PartitionKey:      "library",
		EmbeddingModel:    testEmbeddingModel{},
		DocumentBatcher:   testBatcher{},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(store != nil)
	// Output: true
}
