package clickhouse

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNativeRollbackOutlivesCallerCancellation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("BEGIN TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	ctx, cancel := context.WithCancel(t.Context())
	transaction, err := newNativeTransaction(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := transaction.Close(); err != nil {
		t.Fatal(err)
	}
	mock.ExpectClose()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUncertainNativeBoundaryDiscardsPhysicalSession(t *testing.T) {
	for _, failBegin := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed commit and rollback", true: "failed begin"}[failBegin], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			boundaryFailure := errors.New("native boundary failed")
			rollbackFailure := errors.New("native rollback failed")
			if failBegin {
				mock.ExpectExec("BEGIN TRANSACTION").WillReturnError(boundaryFailure)
				mock.ExpectClose()
				if _, err := newNativeTransaction(t.Context(), db); !errors.Is(err, boundaryFailure) {
					t.Fatal(err)
				}
			} else {
				mock.ExpectExec("BEGIN TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
				transaction, err := newNativeTransaction(t.Context(), db)
				if err != nil {
					t.Fatal(err)
				}
				mock.ExpectExec("COMMIT").WillReturnError(boundaryFailure)
				if err := transaction.Commit(); !errors.Is(err, boundaryFailure) {
					t.Fatal(err)
				}
				mock.ExpectExec("ROLLBACK").WillReturnError(rollbackFailure)
				mock.ExpectClose()
				if err := transaction.Close(); !errors.Is(err, rollbackFailure) {
					t.Fatal(err)
				}
			}
			if db.Stats().OpenConnections != 0 {
				t.Fatal("uncertain physical session returned to the pool")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
