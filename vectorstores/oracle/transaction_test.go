package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type transactionFixture struct {
	prepareErr  error
	deleteErr   error
	commitErr   error
	rollbackErr error
	panicValue  any
	commits     int
	rollbacks   int
	deletions   int
}

type transactionConnector struct{ fixture *transactionFixture }

func (t transactionConnector) Connect(context.Context) (driver.Conn, error) {
	return &transactionConnection{fixture: t.fixture}, nil
}

func (t transactionConnector) Driver() driver.Driver { return resultDriver{} }

type transactionConnection struct{ fixture *transactionFixture }

func (t *transactionConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}

func (t *transactionConnection) Close() error { return nil }

func (t *transactionConnection) Begin() (driver.Tx, error) {
	return &transactionOutcome{fixture: t.fixture}, nil
}

func (t *transactionConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return t.Begin()
}

func (t *transactionConnection) QueryContext(ctx context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(statement, "SELECT CASE WHEN COUNT(*)") {
		if t.fixture.prepareErr != nil {
			return nil, t.fixture.prepareErr
		}
		return &resultRows{cols: []string{"allowed"}, values: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(statement, "TABLE_COMMENT") || strings.Contains(statement, "SELECT VECTOR_INFO") || strings.Contains(statement, "SELECT COUNT(*) FROM ALL_CONSTRAINTS") || strings.Contains(statement, "NLS_DATABASE_PARAMETERS") {
		return (&resultConnection{}).QueryContext(ctx, statement, args)
	}
	return &resultRows{cols: []string{"id", "facts"}, values: [][]driver.Value{{[]byte("victim"), []byte(`{"tenant":"alpha"}`)}}}, nil
}

func (t *transactionConnection) ExecContext(_ context.Context, statement string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(statement, "DELETE FROM ") {
		t.fixture.deletions++
		if t.fixture.panicValue != nil {
			panic(t.fixture.panicValue)
		}
		if t.fixture.deleteErr != nil {
			return nil, t.fixture.deleteErr
		}
	}
	return driver.RowsAffected(1), nil
}

type transactionOutcome struct{ fixture *transactionFixture }

func (t *transactionOutcome) Commit() error {
	t.fixture.commits++
	return t.fixture.commitErr
}

func (t *transactionOutcome) Rollback() error {
	t.fixture.rollbacks++
	return t.fixture.rollbackErr
}

func TestFilteredDeletionCommitsOnlyAfterNormalSuccess(t *testing.T) {
	failure := errors.New("native failure")
	cleanupFailure := errors.New("cleanup failure")
	panicValue := &struct{ message string }{"native panic"}
	for _, test := range []struct {
		name                          string
		fixture                       transactionFixture
		wantErr                       error
		commits, rollbacks, deletions int
	}{
		{name: "success", commits: 1, deletions: 1},
		{name: "prepare failure", fixture: transactionFixture{prepareErr: failure}, wantErr: failure, rollbacks: 1},
		{name: "delete failure", fixture: transactionFixture{deleteErr: failure}, wantErr: failure, rollbacks: 1, deletions: 1},
		{name: "panic after delete", fixture: transactionFixture{panicValue: panicValue}, rollbacks: 1, deletions: 1},
		{name: "commit failure", fixture: transactionFixture{commitErr: failure}, wantErr: failure, commits: 1, deletions: 1},
		{name: "cleanup failure", fixture: transactionFixture{deleteErr: failure, rollbackErr: cleanupFailure}, wantErr: failure, rollbacks: 1, deletions: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := sql.OpenDB(transactionConnector{fixture: &test.fixture})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			store := offlineStore(t, db)
			var recovered any
			var err error
			func() {
				defer func() { recovered = recover() }()
				err = store.DeleteMatching(t.Context(), filter.EQ("tenant", "alpha"), fixtureBudget())
			}()
			if recovered != test.fixture.panicValue || !errors.Is(err, test.wantErr) {
				t.Fatalf("panic=%v error=%v", recovered, err)
			}
			if test.fixture.rollbackErr != nil && !errors.Is(err, test.fixture.rollbackErr) {
				t.Fatalf("cleanup cause lost: %v", err)
			}
			if test.fixture.commits != test.commits || test.fixture.rollbacks != test.rollbacks || test.fixture.deletions != test.deletions {
				t.Fatalf("commits=%d rollbacks=%d deletes=%d", test.fixture.commits, test.fixture.rollbacks, test.fixture.deletions)
			}
		})
	}
}
