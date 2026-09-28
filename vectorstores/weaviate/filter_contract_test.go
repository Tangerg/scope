package weaviate

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type filterFixture struct {
	t       *testing.T
	mu      sync.Mutex
	docs    []*document.Document
	deleted []string
	queries []string
	failure string
	cancel  context.CancelFunc
}

func (f *filterFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case request.URL.Path == "/v1/meta":
		_, _ = writer.Write([]byte(`{"version":"1.39.3"}`))
	case strings.HasPrefix(request.URL.Path, "/v1/schema"):
		_, _ = writer.Write([]byte(`{"class":"Documents","vectorizer":"none","vectorIndexConfig":{"distance":"cosine"},"properties":[{"name":"content","dataType":["text"],"tokenization":"word"},{"name":"metadata","dataType":["text"]}]}`))
	case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/objects/"):
		id := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		f.deleted = append(f.deleted, id)
		writer.WriteHeader(http.StatusNoContent)
	case request.URL.Path == "/v1/graphql":
		var body struct {
			Query string `json:"query"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			f.t.Error(err)
			http.Error(writer, "invalid query", http.StatusBadRequest)
			return
		}
		f.queries = append(f.queries, body.Query)
		var items []map[string]any
		if strings.Contains(body.Query, "nearVector:") || strings.Contains(body.Query, "hybrid:") {
			if !strings.Contains(body.Query, `path: ["id"]`) || !strings.Contains(body.Query, "operator: ContainsAny") {
				f.t.Errorf("filtered ranking was not restricted to exact IDs: %s", body.Query)
				http.Error(writer, "expected UUID filter", http.StatusBadRequest)
				return
			}
			match := regexp.MustCompile(`valueText:\s*(\[[^]]*\])`).FindStringSubmatch(body.Query)
			var ids []string
			if len(match) != 2 || jsonv2.Unmarshal([]byte(match[1]), &ids) != nil {
				f.t.Errorf("invalid UUID filter: %s", body.Query)
				return
			}
			limit := len(f.docs)
			if match := regexp.MustCompile(`limit:\s*(\d+)`).FindStringSubmatch(body.Query); len(match) == 2 {
				limit, _ = strconv.Atoi(match[1])
			}
			for _, doc := range f.docs {
				if slices.Contains(ids, doc.ID) && len(items) < limit {
					items = append(items, f.item(doc))
				}
			}
			if len(items) > 0 {
				switch f.failure {
				case "changed metadata":
					items[0][fieldMetadata] = `{"value":"changed"}`
				case "changed type":
					items[0][fieldMetadata] = `{"value":42}`
				case "unexpected ID":
					items[0]["_additional"].(map[string]any)[additionalID] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
				}
			}
		} else {
			after := ""
			if match := regexp.MustCompile(`after:\s*"([^"]+)"`).FindStringSubmatch(body.Query); len(match) == 2 {
				after = match[1]
			}
			if after != "" {
				switch f.failure {
				case "repeated cursor":
					after = ""
				case "page failure":
					_, _ = writer.Write([]byte(`{"errors":[{"message":"page unavailable"}]}`))
					return
				case "canceled":
					f.cancel()
					return
				}
			}
			for _, doc := range f.docs {
				if doc.ID > after && len(items) < 2 {
					item := f.item(doc)
					if f.failure == "malformed metadata" && after != "" {
						item[fieldMetadata] = "invalid JSON"
					}
					items = append(items, item)
				}
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		encoded, err := jsonv2.Marshal(map[string]any{"data": map[string]any{"Get": map[string]any{"Documents": items}}})
		if err != nil {
			f.t.Error(err)
			return
		}
		_, _ = writer.Write(encoded)
	default:
		f.t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
		http.NotFound(writer, request)
	}
}

func (f *filterFixture) item(doc *document.Document) map[string]any {
	encoded, err := jsonv2.Marshal(doc.Metadata)
	if err != nil {
		f.t.Error(err)
	}
	return map[string]any{fieldContent: doc.Text, fieldMetadata: string(encoded), "_additional": map[string]any{
		additionalID: doc.ID, additionalDistance: float64(0), additionalScore: "1",
	}}
}

func (f *filterFixture) snapshot() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted), slices.Clone(f.queries)
}

func newFilterStore(t *testing.T, docs []*document.Document, failure string) (*Store, *filterFixture) {
	t.Helper()
	fixture := &filterFixture{t: t, docs: docs, failure: failure}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := weaviateclient.NewClient(weaviateclient.Config{Host: strings.TrimPrefix(server.URL, "http://"), Scheme: "http"})
	if err != nil {
		t.Fatal(err)
	}
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for index := range outputs {
			outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, ClassName: "Documents", EmbeddingModel: model, DocumentBatcher: testBatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	return store, fixture
}

func TestFilterSelectionUsesCompleteMetadata(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		name := "search"
		if deletion {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				store, fixture := newFilterStore(t, docs, "")
				if deletion {
					if err := store.DeleteWhere(ctx, predicate); err != nil {
						return nil, err
					}
					deleted, _ := fixture.snapshot()
					return deleted, nil
				}
				response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
				if err != nil {
					return nil, err
				}
				var ids []string
				for _, result := range response.Results {
					ids = append(ids, result.Document.ID)
				}
				return ids, nil
			}})
		})
	}
}

func TestFilterSelectionPrecedesTopKAndExhaustsShortPages(t *testing.T) {
	var docs []*document.Document
	for index, value := range []string{"skip", "skip", "skip", "wanted", "wanted"} {
		values, err := metadata.FromValues(map[string]any{"value": value})
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1), Text: "text", Metadata: values})
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		t.Run(string(mode), func(t *testing.T) {
			store, fixture := newFilterStore(t, docs, "")
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{Mode: mode, TopK: 1, Filter: filter.EQ("value", "wanted")}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Document.ID != docs[3].ID {
				t.Fatalf("filtered top result = %#v", response.Results)
			}
			_, queries := fixture.snapshot()
			if len(queries) != 5 || !strings.Contains(queries[len(queries)-1], docs[3].ID) || !strings.Contains(queries[len(queries)-1], docs[4].ID) {
				t.Fatalf("cursor scan and final ID query = %v", queries)
			}
		})
	}
}

func TestDeleteWhereDoesNotMutateBeforeFullSelectionSucceeds(t *testing.T) {
	var docs []*document.Document
	for index := range 3 {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1), Text: "text"})
	}
	for _, failure := range []string{"malformed metadata", "repeated cursor", "page failure", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			store, fixture := newFilterStore(t, docs, failure)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fixture.cancel = cancel
			err := store.DeleteWhere(ctx, filter.IsNull("value"))
			if err == nil {
				t.Fatalf("DeleteWhere accepted %s on a later page", failure)
			}
			if failure == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("DeleteWhere error = %v, want context.Canceled", err)
			}
			deleted, _ := fixture.snapshot()
			if len(deleted) != 0 {
				t.Fatalf("deleted before complete successful selection: %v", deleted)
			}
		})
	}
}

func TestDeleteWherePropagatesPredicateTypeErrorBeforeMutation(t *testing.T) {
	var docs []*document.Document
	for index, value := range []any{"a", "a", 42} {
		values, err := metadata.FromValues(map[string]any{"value": value})
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1), Text: "text", Metadata: values})
	}
	store, fixture := newFilterStore(t, docs, "")
	if err := store.DeleteWhere(t.Context(), filter.Like("value", "a%")); err == nil {
		t.Fatal("DeleteWhere accepted LIKE against a number")
	}
	deleted, queries := fixture.snapshot()
	if len(deleted) != 0 || len(queries) != 2 {
		t.Fatalf("predicate failure: deleted = %v, queries = %v", deleted, queries)
	}
}

func TestFilteredSearchRejectsChangedOrUnselectedResults(t *testing.T) {
	values, err := metadata.FromValues(map[string]any{"value": "wanted"})
	if err != nil {
		t.Fatal(err)
	}
	docs := []*document.Document{{ID: "00000000-0000-4000-8000-000000000001", Text: "text", Metadata: values}}
	for _, failure := range []string{"changed metadata", "changed type", "unexpected ID"} {
		t.Run(failure, func(t *testing.T) {
			store, _ := newFilterStore(t, docs, failure)
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.Like("value", "wanted")}})
			if err == nil || response != nil {
				t.Fatalf("Search accepted %s: response = %#v, error = %v", failure, response, err)
			}
		})
	}
}
