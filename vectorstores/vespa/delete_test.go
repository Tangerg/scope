package vespa

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestDeleteWhereRestartsSearchAfterMutation(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	remaining := []string{"first", "second"}
	searches := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/search/":
			searches++
			var body map[string]any
			if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
				t.Errorf("decode search request: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, exists := body["offset"]; exists {
				t.Error("search request must restart from the first hit after deleting documents")
			}

			children := make([]map[string]any, 0, len(remaining))
			for _, id := range remaining {
				children = append(children, map[string]any{"id": "id:scope:document::" + id, "fields": map[string]any{"doc_id": id}})
			}
			_ = jsonv2.MarshalWrite(writer, map[string]any{
				"root": map[string]any{
					"children": children,
					"coverage": map[string]any{"coverage": 100, "full": true},
				},
			})

		case request.Method == http.MethodDelete:
			for index, id := range remaining {
				if request.URL.Path == fmt.Sprintf("/document/v1/scope/document/docid/%s", id) {
					remaining = append(remaining[:index], remaining[index+1:]...)
					_ = jsonv2.MarshalWrite(writer, map[string]any{})
					return
				}
			}
			writer.WriteHeader(http.StatusNotFound)

		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	predicate, err := filter.Parse(`tenant == 'scope'`)
	if err != nil {
		t.Fatalf("parse filter: %v", err)
	}
	store := &Store{
		endpoint:   server.URL,
		schemaName: "document",
		namespace:  "scope",
		idField:    "doc_id",
		httpClient: server.Client(),
	}
	if err := store.DeleteWhere(t.Context(), predicate); err != nil {
		t.Fatalf("DeleteWhere: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(remaining) != 0 {
		t.Fatalf("remaining documents = %v, want none", remaining)
	}
	if searches != 2 {
		t.Fatalf("search requests = %d, want 2", searches)
	}
}

func TestDeleteWhereRejectsForeignAndRepeatedHits(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign=%t", foreign), func(t *testing.T) {
			searches, deletes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodDelete {
					deletes++
					fmt.Fprint(writer, `{}`)
					return
				}
				searches++
				var body struct {
					YQL string `json:"yql"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
				}
				if !strings.Contains(body.YQL, `scope_namespace contains "scope"`) {
					t.Errorf("unscoped query: %s", body.YQL)
				}
				namespace := "scope"
				if foreign {
					namespace = "other"
				}
				fmt.Fprintf(writer, `{"root":{"coverage":{"coverage":100,"full":true},"children":[{"id":"id:%s:document::same","fields":{"doc_id":"same"}}]}}`, namespace)
			}))
			defer server.Close()
			store := &Store{endpoint: server.URL, schemaName: "document", namespace: "scope", idField: "doc_id", httpClient: server.Client()}
			err := store.DeleteWhere(t.Context(), filter.EQ("tenant", "scope"))
			if err == nil {
				t.Fatal("unsafe/repeated hit accepted")
			}
			if foreign && (deletes != 0 || searches != 1) {
				t.Fatalf("foreign hit: deletes=%d searches=%d", deletes, searches)
			}
			if !foreign && (deletes != 1 || searches != 2) {
				t.Fatalf("repeated hit: deletes=%d searches=%d", deletes, searches)
			}
		})
	}
}
