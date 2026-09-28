package opensearch

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	opensearchsdk "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// The server stores raw fixture documents and implements only pagination, ID
// selection and bulk acknowledgments. It never evaluates a metadata predicate.
type filterFixture struct {
	documents []*document.Document
	page      int
	deleted   []string
}

func (f *filterFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodDelete {
		_, _ = io.WriteString(w, `{"succeeded":true,"num_freed":1}`)
		return
	}
	if strings.HasSuffix(r.URL.Path, "_bulk") {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		items := make([]map[string]any, 0)
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			var action bulkAction
			if decodeErr := jsonv2.Unmarshal([]byte(line), &action); decodeErr != nil || action.Delete == nil {
				http.Error(w, "invalid bulk action", 400)
				return
			}
			f.deleted = append(f.deleted, action.Delete.ID)
			items = append(items, map[string]any{"delete": map[string]any{"_id": action.Delete.ID, "status": 200}})
		}
		encoded, err := jsonv2.Marshal(map[string]any{"errors": false, "items": items})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write(encoded)
		return
	}
	selected := f.documents
	scan := r.URL.Query().Get("scroll") != "" || strings.HasSuffix(r.URL.Path, "_search/scroll")
	if scan {
		start := min(f.page*2, len(selected))
		selected = selected[start:min(start+2, len(selected))]
		f.page++
	} else {
		var request searchRequest
		if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil || request.Query.KNN["embedding"].Filter == nil {
			http.Error(w, "missing IDs", 400)
			return
		}
		selected = nil
		for _, doc := range f.documents {
			if slices.Contains(request.Query.KNN["embedding"].Filter.IDs.Values, doc.ID) {
				selected = append(selected, doc)
			}
		}
	}
	hits := make([]map[string]any, 0, len(selected))
	for _, doc := range selected {
		hits = append(hits, map[string]any{"_id": doc.ID, "_seq_no": 1, "_primary_term": 1, "_score": 0.8, "_source": map[string]any{"metadata": doc.Metadata, "content": doc.Text}})
	}
	body := map[string]any{"hits": map[string]any{"hits": hits}}
	if scan {
		body["_scroll_id"] = "fixture-snapshot"
	}
	encoded, err := jsonv2.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, _ = w.Write(encoded)
}

func TestFilterConformance(t *testing.T) {
	for _, operation := range []string{"search", "delete"} {
		t.Run(operation, func(t *testing.T) {
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				fixture := &filterFixture{documents: docs}
				server := httptest.NewServer(fixture)
				defer server.Close()
				client, err := opensearchapi.NewClient(opensearchapi.Config{Client: opensearchsdk.Config{Addresses: []string{server.URL}, Transport: server.Client().Transport}})
				if err != nil {
					return nil, err
				}
				model, err := embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
					return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
				}))
				if err != nil {
					return nil, err
				}
				store := &Store{client: client, indexName: "documents", metadataField: "metadata", contentField: "content", embeddingField: "embedding", embeddingClient: model, engine: EngineLucene, spaceType: SpaceTypeCosine}
				if operation == "delete" {
					if deleteErr := store.DeleteWhere(ctx, predicate); deleteErr != nil {
						return nil, deleteErr
					}
					return fixture.deleted, nil
				}
				response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "conformance", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
				if err != nil {
					return nil, err
				}
				if fixture.page < 2 {
					return nil, fmt.Errorf("metadata pagination was not exhausted")
				}
				ids := make([]string, len(response.Results))
				for index, result := range response.Results {
					ids[index] = result.Document.ID
				}
				return ids, nil
			}})
		})
	}
}
