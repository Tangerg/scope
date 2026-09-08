package vectara

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
)

// Vectara owns embedding and scoring, so unlike every other store here there
// is no metric to agree on. What a caller can still get wrong is which corpus
// they are pointed at, and Vectara's own reason for reporting the enabled flag
// is that an administrator may disable a corpus to respond to an incident. A
// store that kept operating against one would be working around that decision.
func TestNewStoreRefusesADisabledCorpus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		status  int
		wantErr error
	}{
		{name: "enabled", body: `{"key":"corpus","enabled":true}`, status: http.StatusOK},
		{name: "flag absent", body: `{"key":"corpus"}`, status: http.StatusOK},
		{name: "disabled", body: `{"key":"corpus","enabled":false}`, status: http.StatusOK, wantErr: ErrUnavailableCorpus},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				paths = append(paths, request.Method+" "+request.URL.Path)
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)

			_, err := NewStore(t.Context(), StoreConfig{
				Endpoint: server.URL, APIKey: "test", CorpusKey: "corpus",
				DocumentBatcher: passthroughBatcher{},
				HTTPClient:      server.Client(),
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("NewStore() = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewStore() = %v, want nil", err)
			}
			if len(paths) != 1 || !strings.HasSuffix(paths[0], "/v2/corpora/corpus") {
				t.Fatalf("construction requests = %v, want one read of the corpus", paths)
			}
		})
	}
}

// A wrong corpus key surfaces as the read's own failure, which is the point of
// doing the read: otherwise it arrives on the first upload, far from the
// wiring that names the key.
func TestNewStoreReportsAnUnknownCorpus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"messages":["Corpus not found."]}`))
	}))
	t.Cleanup(server.Close)

	_, err := NewStore(t.Context(), StoreConfig{
		Endpoint: server.URL, APIKey: "test", CorpusKey: "typo",
		DocumentBatcher: passthroughBatcher{},
		HTTPClient:      server.Client(),
	})
	if err == nil {
		t.Fatal("NewStore() = nil error, want the corpus read to fail")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Fatalf("NewStore() = %v, want an error naming the corpus key", err)
	}
}

// passthroughBatcher satisfies the Batcher contract for a construction test,
// where no document is ever indexed.
type passthroughBatcher struct{}

func (passthroughBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	return [][]*document.Document{documents}, nil
}
