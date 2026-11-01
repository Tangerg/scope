package storetest

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Capabilities is the exact set of operations a backend implements.
// Native mode, filter and media constraints are checked on a configured store;
// these depend on its bound schema, not on method presence.
type Capabilities struct {
	Upserter      bool
	Querier       bool
	IDDeleter     bool
	FilterDeleter bool

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
	assertCapability(t, "Upserter", actual.Upserter, expected.Upserter)
	assertCapability(t, "Querier", actual.Querier, expected.Querier)
	if upsert, ok := store.(vectorstore.Upserter); ok {
		if err := upsert.Upsert(t.Context(), nil); !errors.Is(err, vectorstore.ErrInvalidRequest) {
			t.Errorf("nil vector batch: %v", err)
		}
		b := &vectorstore.Batch{Space: "unbound", Records: []vectorstore.Record{{Document: &document.Document{ID: "fixture", Text: "text"}, Values: []float64{1}}}}
		if err := upsert.Upsert(t.Context(), b); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
			t.Errorf("unbound vector batch: %v", err)
		}
	}
	if query, ok := store.(vectorstore.Querier); ok {
		if _, err := query.Query(t.Context(), nil); !errors.Is(err, vectorstore.ErrInvalidRequest) {
			t.Errorf("nil vector query: %v", err)
		}
		q := &vectorstore.Query{Space: "unbound", Values: []float64{1}}
		if r, err := query.Query(t.Context(), q); r != nil || !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
			t.Errorf("unbound vector query: %v %v", r, err)
		}
	}
	assertCapability(t, "IDDeleter", actual.IDDeleter, expected.IDDeleter)
	assertCapability(t, "FilterDeleter", actual.FilterDeleter, expected.FilterDeleter)
	assertCapability(t, "Closer", actual.Closer, expected.Closer)

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

// CapabilitiesOf detects the operations implemented by store.
func CapabilitiesOf(store any) Capabilities {
	_, upserter := store.(vectorstore.Upserter)
	_, querier := store.(vectorstore.Querier)

	_, idDeleter := store.(vectorstore.IDDeleter)
	_, filterDeleter := store.(vectorstore.FilterDeleter)
	_, closer := store.(vectorstore.Closer)
	return Capabilities{
		Upserter: upserter, Querier: querier,

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
