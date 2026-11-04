package azureaisearch

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestSpaceFilterAndScanBudget(t *testing.T) {
	f := newProtocolFixture(t)
	s := f.store()
	f.put(&document.Document{ID: "a", Text: "a"}, &document.Document{ID: "b", Text: "b"})
	for _, test := range []struct {
		space  vectorstore.SpaceID
		filter filter.Predicate
		want   error
	}{
		{"other", nil, vectorstore.ErrIncompatibleSpace}, {testSpace, filter.EQ("nonmatch", 1), vectorstore.ErrUnsupportedFilter},
	} {
		q := &vectorstore.Query{Space: test.space, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{Filter: test.filter}}
		if found, err := s.Query(t.Context(), q); found != nil || !errors.Is(err, test.want) {
			t.Fatalf("query=%v, err=%v", found, err)
		}
	}
	if len(f.bodies) != 0 {
		t.Fatal("a rejected query read data")
	}
	for _, records := range []bool{false, true} {
		request := testScanRequest(filter.EQ("missing", 42))
		if records {
			request.Budget.MaxRecords = 1
		} else {
			request.Budget.MaxBytes = 1
		}
		if out, err := s.Scan(t.Context(), request); out != nil || !errors.Is(err, vectorstore.ErrScanBudget) {
			t.Fatalf("scan=%v, err=%v", out, err)
		}
	}
	c := f.config()
	c.Space = "other"
	if _, err := NewStore(t.Context(), c); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatalf("different space bound same native field: %v", err)
	}
	f.mu.Lock()
	f.schema = nativeSchema("euclidean")
	f.mu.Unlock()
	if err := s.Upsert(t.Context(), testBatch([]*document.Document{{ID: "a", Text: "a"}})); !errors.Is(err, vectorstore.ErrIncompatibleSpace) || len(f.writes) != 0 {
		t.Fatalf("admin change accepted: %v", err)
	}
}
func TestPerKeyWriteEvidence(t *testing.T) {
	f := newProtocolFixture(t)
	s := f.store()
	f.write = func(w http.ResponseWriter, _ []metadata.Map) {
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`{"value":[{"key":"unknown","status":true,"statusCode":500},{"key":"ok","status":true,"statusCode":201},{"key":"rejected","status":false,"statusCode":400}]}`))
	}
	docs := []*document.Document{{ID: "ok", Text: "a"}, {ID: "rejected", Text: "b"}, {ID: "unknown", Text: "c"}}
	var write *vectorstore.WriteError
	if err := s.Upsert(t.Context(), testBatch(docs)); !errors.As(err, &write) || !slices.Equal(write.Acknowledged, []string{"ok"}) || !slices.Equal(write.Rejected, []string{"rejected"}) || !slices.Equal(write.Unconfirmed, []string{"unknown"}) || len(write.Unattempted) != 0 {
		t.Fatalf("write evidence: %#v", write)
	}
}
