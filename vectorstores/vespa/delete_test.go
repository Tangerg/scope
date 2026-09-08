package vespa

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode search request: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, exists := body["offset"]; exists {
				t.Error("search request must restart from the first hit after deleting documents")
			}

			children := make([]map[string]any, 0, len(remaining))
			for _, id := range remaining {
				children = append(children, map[string]any{"fields": map[string]any{"doc_id": id}})
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"root": map[string]any{
					"children": children,
					"coverage": map[string]any{"coverage": 100, "full": true},
				},
			})

		case request.Method == http.MethodDelete:
			for index, id := range remaining {
				if request.URL.Path == fmt.Sprintf("/document/v1/scope/document/docid/%s", id) {
					remaining = append(remaining[:index], remaining[index+1:]...)
					_ = json.NewEncoder(writer).Encode(map[string]any{})
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
