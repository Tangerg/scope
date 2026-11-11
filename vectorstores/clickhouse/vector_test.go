package clickhouse

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestVectorAdmissionNeedsNoDatabaseCall(t *testing.T) {
	s, _ := mockStore(t)
	for _, tc := range []struct {
		space  vectorstore.SpaceID
		coords []float64
		p      filter.Predicate
		e      error
	}{
		{"other", []float64{1, 0}, nil, vectorstore.ErrIncompatibleSpace}, {testSpace, []float64{1}, nil, vectorstore.ErrIncompatibleSpace},
		{testSpace, []float64{1, 0}, filter.IsNull("unindexed"), vectorstore.ErrUnsupportedFilter}, {testSpace, []float64{math.MaxFloat64, 0}, nil, vectorstore.ErrInvalidRequest},
	} {
		q := &vectorstore.Query{Space: tc.space, Values: tc.coords, Options: vectorstore.SearchOptions{Filter: tc.p}}
		if got, err := s.Query(t.Context(), q); got != nil || !errors.Is(err, tc.e) {
			t.Fatalf("got=%v, err=%v", got, err)
		}
	}
	b := testBatch([]*document.Document{{ID: "one", Text: "one"}, {ID: "two", Text: "two"}})
	b.Records[1].Values = []float64{math.SmallestNonzeroFloat64, 0}
	if err := s.Upsert(t.Context(), b); !errors.Is(err, vectorstore.ErrInvalidRequest) {
		t.Fatal(err)
	}
}

func TestLostInsertReceiptIsUnconfirmed(t *testing.T) {
	s, m := mockStore(t)
	m.ExpectBegin()
	stmt := m.ExpectPrepare("INSERT INTO scope.documents")
	for _, id := range []string{"a", "b"} {
		stmt.ExpectExec().WithArgs(id, id, "null", []float32{1, 0}).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	lost := errors.New("ack lost")
	m.ExpectCommit().WillReturnError(lost)
	var failure *vectorstore.WriteError
	err := s.Upsert(t.Context(), testBatch([]*document.Document{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}}))
	if !errors.Is(err, lost) || !errors.As(err, &failure) || !slices.Equal(failure.Outcome().Unconfirmed, []string{"a", "b"}) || len(failure.Outcome().Acknowledged) != 0 {
		t.Fatalf("evidence=%#v error=%v", failure, err)
	}
}

func TestBudgetStopsAtomicDeleteBeforeMutation(t *testing.T) {
	for _, bytes := range []bool{false, true} {
		s, m := mockStore(t)
		m.ExpectExec("BEGIN TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
		m.ExpectQuery("SELECT id, content, metadata").WillReturnRows(sqlmock.NewRows([]string{"id", "content", "metadata"}).AddRow("a", "a", "null").AddRow("b", "b", "null")).RowsWillBeClosed()
		m.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
		request := testScanRequest(filter.IsNull("absent"))
		if bytes {
			request.Budget.MaxBytes = 1
		} else {
			request.Budget.MaxRecords = 1
		}
		if err := s.DeleteMatching(t.Context(), request.Filter, request.Budget); !errors.Is(err, vectorstore.ErrScanBudget) {
			t.Fatal(err)
		}
	}
}
