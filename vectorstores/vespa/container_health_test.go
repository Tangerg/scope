package vespa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
)

// The schema and rank profile live in an application package this store cannot
// read, so there is no configured metric to compare against a live one. What
// the container does report is whether it can answer at all, and Vespa
// distinguishes "initializing" -- a query container still waiting on content
// nodes -- from "up". A store built against either of the other two states
// would fail every request for a reason unrelated to the caller's query.
func TestNewStoreRefusesAContainerThatIsNotServing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "up", body: `{"time":1661863544346,"status":{"code":"up"}}`},
		{name: "initializing", body: `{"status":{"code":"initializing","message":"waiting for content nodes"}}`, wantErr: true},
		{name: "down", body: `{"status":{"code":"down"}}`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				paths = append(paths, request.Method+" "+request.URL.Path)
				_, _ = writer.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)

			_, err := NewStore(t.Context(), StoreConfig{
				Endpoint: server.URL, SchemaName: "documents", Namespace: "documents",
				RankingProfile:  "closeness_profile",
				EmbeddingModel:  healthTestEmbeddingModel{},
				DocumentBatcher: healthTestBatcher{},
				HTTPClient:      server.Client(),
			})
			if test.wantErr {
				if !errors.Is(err, ErrUnavailableContainer) {
					t.Fatalf("NewStore() = %v, want ErrUnavailableContainer", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewStore() = %v, want nil", err)
			}
			if len(paths) != 1 || !strings.HasSuffix(paths[0], "/state/v1/health") {
				t.Fatalf("construction requests = %v, want one health read", paths)
			}
		})
	}
}

type healthTestEmbeddingModel struct{}

func (healthTestEmbeddingModel) Call(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, len(request.Texts))
	for index := range outputs {
		outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
	}
	return embedding.NewResponse(outputs, nil)
}

type healthTestBatcher struct{}

func (healthTestBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	return [][]*document.Document{documents}, nil
}
