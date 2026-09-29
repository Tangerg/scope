package storetest

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Capabilities is the exact interface, content, and search-semantics set a backend
// promises. A false interface flag forbids accidental implementation; a false
// HybridSearch flag requires rejection before external I/O.
type Capabilities struct {
	Indexer      bool
	Searcher     bool
	HybridSearch bool
	// MediaDocuments declares lossless storage of document media alongside text.
	// A false value requires rejection of mixed content before external I/O.
	MediaDocuments bool
	IDDeleter      bool
	FilterDeleter  bool

	// Closer is true only when the store owns a resource it must release.
	// A store using caller-owned resources must not expose a no-op Close.
	Closer bool
}

// Run verifies the backend's exact capability set and the common operations
// that must complete before external I/O. Pass a non-nil zero-value *Store;
// the calls below must not reach provider dependencies.
func Run(t *testing.T, store any, expected Capabilities) {
	t.Helper()
	if store == nil {
		t.Fatal("conformance: store must not be nil")
	}

	actual := CapabilitiesOf(store)
	assertCapability(t, "Indexer", actual.Indexer, expected.Indexer)
	assertCapability(t, "Searcher", actual.Searcher, expected.Searcher)
	assertCapability(t, "IDDeleter", actual.IDDeleter, expected.IDDeleter)
	assertCapability(t, "FilterDeleter", actual.FilterDeleter, expected.FilterDeleter)
	assertCapability(t, "Closer", actual.Closer, expected.Closer)

	if indexer, ok := store.(vectorstore.Indexer); ok && expected.Indexer {
		runIndexerChecks(t, indexer, expected.MediaDocuments)
	}
	if searcher, ok := store.(vectorstore.Searcher); ok && expected.Searcher {
		runSearcherChecks(t, searcher, expected.HybridSearch)
	}
	if deleter, ok := store.(vectorstore.IDDeleter); ok && expected.IDDeleter {
		t.Run("DeleteIDsTreatsEmptyInputAsNoop", func(t *testing.T) {
			if err := deleter.DeleteIDs(t.Context(), nil); err != nil {
				t.Fatalf("DeleteIDs(nil) error = %v, want nil", err)
			}
		})
	}
	if deleter, ok := store.(vectorstore.FilterDeleter); ok && expected.FilterDeleter {
		t.Run("DeleteWhereRejectsMissingFilterBeforeIO", func(t *testing.T) {
			if err := deleter.DeleteWhere(t.Context(), nil); !errors.Is(err, vectorstore.ErrMissingFilter) {
				t.Fatalf("DeleteWhere(nil) error = %v, want %v", err, vectorstore.ErrMissingFilter)
			}
		})
	}
}

func runIndexerChecks(t *testing.T, indexer vectorstore.Indexer, mediaDocuments bool) {
	t.Helper()
	indexCases := []struct {
		name string
		docs []*document.Document
		want error
	}{
		{name: "empty documents", want: vectorstore.ErrEmptyDocuments},
		{name: "nil document", docs: []*document.Document{nil}, want: vectorstore.ErrInvalidDocument},
		{name: "missing document ID", docs: []*document.Document{{Text: "content"}}, want: vectorstore.ErrMissingDocumentID},
		{
			name: "duplicate document ID",
			docs: []*document.Document{{ID: "duplicate", Text: "one"}, {ID: "duplicate", Text: "two"}},
			want: vectorstore.ErrDuplicateDocumentID,
		},
	}
	for _, test := range indexCases {
		t.Run("IndexRejects"+test.name+"BeforeIO", func(t *testing.T) {
			request := &vectorstore.IndexRequest{Documents: test.docs}
			if err := indexer.Index(t.Context(), request); !errors.Is(err, test.want) {
				t.Fatalf("Index() error = %v, want %v", err, test.want)
			}
		})
	}
	if mediaDocuments {
		return
	}
	t.Run("IndexRejectsUnsupportedMediaBeforeIO", func(t *testing.T) {
		content, err := media.NewURI("image/png", "https://example.com/image.png")
		if err != nil {
			t.Fatal(err)
		}
		request := &vectorstore.IndexRequest{Documents: []*document.Document{
			{ID: "1", Text: "text-only document"},
			{ID: "2", Text: "caption", Media: content},
		}}
		if indexErr := indexer.Index(t.Context(), request); !errors.Is(indexErr, vectorstore.ErrInvalidDocument) {
			t.Fatalf("Index(mixed content) error = %v, want ErrInvalidDocument before I/O", indexErr)
		}
	})
}

func runSearcherChecks(t *testing.T, searcher vectorstore.Searcher, hybridSearch bool) {
	t.Helper()
	t.Run("SearchRejectsInvalidRequestBeforeIO", func(t *testing.T) {
		if _, err := searcher.Search(t.Context(), &vectorstore.SearchRequest{}); err == nil {
			t.Fatal("Search(zero request) error = nil, want validation error")
		}
	})
	if hybridSearch {
		return
	}
	t.Run("SearchRejectsUnsupportedHybridBeforeIO", func(t *testing.T) {
		request := &vectorstore.SearchRequest{
			Query: "query", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid},
		}
		if _, err := searcher.Search(t.Context(), request); !errors.Is(err, vectorstore.ErrUnsupportedSearchMode) {
			t.Fatalf("Search(hybrid) error = %v, want %v", err, vectorstore.ErrUnsupportedSearchMode)
		}
	})
}

// CapabilitiesOf detects interface capabilities. Run probes HybridSearch and
// MediaDocuments through operations because no interface expresses them.
func CapabilitiesOf(store any) Capabilities {
	_, indexer := store.(vectorstore.Indexer)
	_, searcher := store.(vectorstore.Searcher)
	_, idDeleter := store.(vectorstore.IDDeleter)
	_, filterDeleter := store.(vectorstore.FilterDeleter)
	_, closer := store.(vectorstore.Closer)
	return Capabilities{
		Indexer:       indexer,
		Searcher:      searcher,
		IDDeleter:     idDeleter,
		FilterDeleter: filterDeleter,
		Closer:        closer,
	}
}

func assertCapability(t *testing.T, name string, got, want bool) {
	t.Helper()
	if got != want {
		t.Errorf("%s capability = %t, want %t", name, got, want)
	}
}
