package clickhouse

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestDeleteWhereUsesOnlyCoreMembership(t *testing.T) {
	store, mock := mockStore(t)
	mock.ExpectExec("BEGIN TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	for pass := 0; pass < 2; pass++ {
		mock.ExpectQuery("SELECT id, content, metadata").WillReturnRows(sqlmock.NewRows([]string{"id", "content", "metadata"}).AddRow("nested", "text", `{"profile":{"items":[7]}}`).AddRow("literal", "text", `{"profile.items.0":7}`)).RowsWillBeClosed()
		if pass == 1 {
			mock.ExpectExec("DELETE FROM").WithArgs([]string{"nested"}).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectQuery("SELECT id, content, metadata").WillReturnRows(sqlmock.NewRows([]string{"id", "content", "metadata"})).RowsWillBeClosed()
	}
	mock.ExpectExec("COMMIT").WillReturnResult(sqlmock.NewResult(0, 0))
	predicate, err := filter.Parse(`profile['items'][0] == 7`)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWhere(t.Context(), predicate); err != nil {
		t.Fatal(err)
	}
}

func TestFilterErrorRejectsDeletionBeforeAnyEffect(t *testing.T) {
	store, mock := mockStore(t)
	mock.ExpectExec("BEGIN TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT id, content, metadata").WillReturnRows(sqlmock.NewRows([]string{"id", "content", "metadata"}).AddRow("first", "text", `{"n":7}`).AddRow("last", "text", `{"n":"wrong"}`)).RowsWillBeClosed()
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := store.DeleteWhere(t.Context(), filter.GT("n", 1)); err == nil {
		t.Fatal("type mismatch silently became non-membership")
	}
}

func TestDeleteRejectsMissingFilterAndEmptyIDsWithoutIO(t *testing.T) {
	store, _ := mockStore(t)
	if err := store.DeleteWhere(t.Context(), nil); !errors.Is(err, vectorstore.ErrMissingFilter) {
		t.Fatal(err)
	}
	if err := store.DeleteIDs(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestLaterDeleteFailureRollsBackAllPages(t *testing.T) {
	store, mock := mockStore(t)
	failure := errors.New("later native delete failed")
	mock.ExpectExec("BEGIN TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM").WillReturnError(failure)
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
	ids := make([]string, metadataPageSize+1)
	for i := range ids {
		ids[i] = "id"
	}
	if err := store.DeleteIDs(t.Context(), ids); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
