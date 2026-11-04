package azurecosmos

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestSpaceRoutesReadsWritesAndDeletes(t *testing.T) {
	f := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
	first := f.store()
	docs := []*document.Document{{ID: "one", Text: "one"}, {ID: "two", Text: "two"}}
	if err := first.Upsert(t.Context(), testBatch(docs)); err != nil {
		t.Fatal(err)
	}
	other, err := NewStore(t.Context(), StoreConfig{Container: f.container, Space: "other", PartitionKey: "library"})
	if err != nil {
		t.Fatal(err)
	}
	if first.partitionKey == other.partitionKey {
		t.Fatal("same physical partition for different spaces")
	}
	if err = other.Upsert(t.Context(), testBatch(docs)); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatal(err)
	}
	for _, rows := range []bool{false, true} {
		request := testScanRequest(filter.EQ("never", 23))
		if rows {
			request.Budget.MaxRecords = 1
		} else {
			request.Budget.MaxBytes = 1
		}
		if got, err := first.Scan(t.Context(), request); got != nil || !errors.Is(err, vectorstore.ErrScanBudget) {
			t.Fatalf("scan: %v, %v", got, err)
		}
		if err := first.DeleteMatching(t.Context(), request.Filter, request.Budget); !errors.Is(err, vectorstore.ErrScanBudget) || len(f.deletes) != 0 {
			t.Fatalf("over-budget delete: %v", err)
		}
	}
	if err := first.DeleteIDs(t.Context(), []string{"one", "one", "absent"}); err != nil || !slices.Equal(f.deletes, []string{"one"}) {
		t.Fatalf("delete %v: %v", f.deletes, err)
	}
	// Put first-space native response onto the second routing key: a faulty
	// backend must not expose it, even when both use the same vector dimension.
	f.mu.Lock()
	f.partition = other.partitionKey
	f.mu.Unlock()
	if got, err := other.Query(t.Context(), &vectorstore.Query{Space: "other", Values: []float64{1, 0}}); got != nil || err == nil {
		t.Fatalf("cross-partition response: %v %v", got, err)
	}
}
func TestSequentialWriteEvidence(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		f := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
		s := f.store()
		f.writeHook = func(w http.ResponseWriter, id string) bool {
			if id != "middle" {
				return false
			}
			w.WriteHeader(status)
			f.respond(w, map[string]string{"code": "error", "message": "failed"})
			return true
		}
		docs := []*document.Document{{ID: "first", Text: "a"}, {ID: "middle", Text: "b"}, {ID: "last", Text: "c"}}
		var failure *vectorstore.WriteError
		if err := s.Upsert(t.Context(), testBatch(docs)); !errors.As(err, &failure) || !slices.Equal(failure.Acknowledged, []string{"first"}) || !slices.Equal(failure.Unattempted, []string{"last"}) {
			t.Fatalf("write evidence: %#v", failure)
		}
		if (status == 400 && !slices.Equal(failure.Rejected, []string{"middle"})) || (status == 500 && !slices.Equal(failure.Unconfirmed, []string{"middle"})) {
			t.Fatalf("status %d evidence: %#v", status, failure)
		}
	}
}
