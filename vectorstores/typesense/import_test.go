package typesense

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/typesense/typesense-go/v3/typesense"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

type importTestBatcher struct{}

func (importTestBatcher) Batch(
	ctx context.Context,
	documents []*document.Document,
) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func newImportTestStore(t *testing.T, body string) *Store {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		writer.WriteHeader(http.StatusOK)
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Errorf("write import response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	store, err := NewStore(t.Context(), StoreConfig{
		Client: typesense.NewClient(
			typesense.WithServer(server.URL),
			typesense.WithAPIKey("test"),
		),
		CollectionName:  "documents",
		DocumentBatcher: importTestBatcher{},
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

// Typesense answers a partially failed import with HTTP 200, so the per-document
// results are the only evidence that a batch was applied.
func TestIndexRequiresEveryDocumentImportResult(t *testing.T) {
	for _, sample := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "all accepted",
			body: "{\"success\":true}\n{\"success\":true}\n",
		},
		{
			name: "one rejected",
			body: "{\"success\":true}\n" +
				"{\"success\":false,\"error\":\"Field `x` is not found in the document.\",\"document\":\"{}\"}\n",
			want: `documents[1] "two": Field ` + "`x`" + ` is not found in the document.`,
		},
		{
			name: "every document rejected",
			body: "{\"success\":false,\"error\":\"bad id\"}\n{\"success\":false,\"error\":\"bad id\"}\n",
			want: `documents[0] "one": bad id`,
		},
		{
			name: "missing result",
			body: "{\"success\":true}\n",
			want: "import returned 1 results for 2 documents",
		},
		{
			name: "extra result",
			body: "{\"success\":true}\n{\"success\":true}\n{\"success\":true}\n",
			want: "import returned 3 results for 2 documents",
		},
		{
			name: "null result",
			body: "{\"success\":true}\nnull\n",
			want: `documents[1] "two" has no import result`,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store := newImportTestStore(t, sample.body)
			err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{
				{ID: "one", Text: "first"},
				{ID: "two", Text: "second"},
			}})
			if sample.want == "" {
				if err != nil {
					t.Fatalf("Index() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("Index() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}
