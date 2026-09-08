package storetest

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Capabilities is the exact interface and search-semantics set a backend
// promises. A false interface flag forbids accidental implementation; a false
// HybridSearch flag requires rejection before external I/O.
type Capabilities struct {
	Indexer       bool
	Searcher      bool
	HybridSearch  bool
	IDDeleter     bool
	FilterDeleter bool

	// Closer is true only for a store that created a resource of its own. A
	// store handed its client, session or pool releases nothing, so a false
	// flag forbids a Close method rather than permitting a no-op one: a no-op
	// Close claims there was something to release and that calling it released
	// it, which leaves a caller unable to tell the two kinds of store apart.
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

	indexer, hasIndexer := store.(vectorstore.Indexer)
	searcher, hasSearcher := store.(vectorstore.Searcher)
	idDeleter, hasIDDeleter := store.(vectorstore.IDDeleter)
	filterDeleter, hasFilterDeleter := store.(vectorstore.FilterDeleter)

	actual := CapabilitiesOf(store)
	assertCapability(t, "Indexer", actual.Indexer, expected.Indexer)
	assertCapability(t, "Searcher", actual.Searcher, expected.Searcher)
	assertCapability(t, "IDDeleter", actual.IDDeleter, expected.IDDeleter)
	assertCapability(t, "FilterDeleter", actual.FilterDeleter, expected.FilterDeleter)
	assertCapability(t, "Closer", actual.Closer, expected.Closer)

	ctx := t.Context()
	if expected.Indexer && hasIndexer {
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
				if err := indexer.Index(ctx, request); !errors.Is(err, test.want) {
					t.Fatalf("Index() error = %v, want %v", err, test.want)
				}
			})
		}
	}
	if expected.Searcher && hasSearcher {
		t.Run("SearchRejectsInvalidRequestBeforeIO", func(t *testing.T) {
			if _, err := searcher.Search(ctx, &vectorstore.SearchRequest{}); err == nil {
				t.Fatal("Search(zero request) error = nil, want validation error")
			}
		})
		if !expected.HybridSearch {
			t.Run("SearchRejectsUnsupportedHybridBeforeIO", func(t *testing.T) {
				request := &vectorstore.SearchRequest{
					Query: "query", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid},
				}
				if _, err := searcher.Search(ctx, request); !errors.Is(err, vectorstore.ErrUnsupportedSearchMode) {
					t.Fatalf("Search(hybrid) error = %v, want %v", err, vectorstore.ErrUnsupportedSearchMode)
				}
			})
		}
	}
	if expected.IDDeleter && hasIDDeleter {
		t.Run("DeleteIDsTreatsEmptyInputAsNoop", func(t *testing.T) {
			if err := idDeleter.DeleteIDs(ctx, nil); err != nil {
				t.Fatalf("DeleteIDs(nil) error = %v, want nil", err)
			}
		})
	}
	if expected.FilterDeleter && hasFilterDeleter {
		t.Run("DeleteWhereRejectsMissingFilterBeforeIO", func(t *testing.T) {
			if err := filterDeleter.DeleteWhere(ctx, nil); !errors.Is(err, vectorstore.ErrMissingFilter) {
				t.Fatalf("DeleteWhere(nil) error = %v, want %v", err, vectorstore.ErrMissingFilter)
			}
		})
	}
}

// CapabilitiesOf reports the capability set a store actually implements.
//
// [Run] compares it against the set the store declares. It is separate from
// that comparison so the interface detection can be exercised on its own:
// HybridSearch is absent because no interface expresses it — it is a search
// semantic that [Run] probes by calling Search.
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
