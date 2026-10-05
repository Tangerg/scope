package pgstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	pgvec "github.com/pgvector/pgvector-go"
)

type fixtureRows struct {
	pgx.Rows
	rows     [][]any
	position int
	readErr  error
	scanErr  error
}

func (f *fixtureRows) Next() bool {
	if f.position >= len(f.rows) {
		return false
	}
	f.position++
	return true
}
func (f *fixtureRows) Scan(destinations ...any) error {
	if f.scanErr != nil {
		return f.scanErr
	}
	row := f.rows[f.position-1]
	if len(row) != len(destinations) {
		return errors.New("native fixture column count")
	}
	for index, value := range row {
		reflect.ValueOf(destinations[index]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}
func (f *fixtureRows) Err() error { return f.readErr }
func (f *fixtureRows) Close()     {}

type fixtureQuery struct {
	query func(string) (pgx.Rows, error)
}

func (f fixtureQuery) Query(_ context.Context, statement string, _ ...any) (pgx.Rows, error) {
	return f.query(statement)
}

type fixtureTransaction struct {
	pgx.Tx
	query       func(string) (pgx.Rows, error)
	commits     int
	rollbacks   int
	commitErr   error
	rollbackErr error
}

func (f *fixtureTransaction) Query(_ context.Context, statement string, _ ...any) (pgx.Rows, error) {
	return f.query(statement)
}
func (f *fixtureTransaction) Commit(context.Context) error   { f.commits++; return f.commitErr }
func (f *fixtureTransaction) Rollback(context.Context) error { f.rollbacks++; return f.rollbackErr }

func currentColumns() [][]any {
	return [][]any{{"id", "bytea", -1, true, false, 1}, {"content", "text", -1, true, false, 2}, {"facts", "bytea", -1, true, false, 3}, {"embedding", "vector", 2, true, false, 4}}
}

func TestNativeSchemaOwnsRequiredShape(t *testing.T) {
	for name, alter := range map[string]func(*fixtureRows, *fixtureRows){
		"current":           func(*fixtureRows, *fixtureRows) {},
		"old text identity": func(columns *fixtureRows, _ *fixtureRows) { columns.rows[0][1] = "text" },
		"old JSONB":         func(columns *fixtureRows, _ *fixtureRows) { columns.rows[2][1] = "jsonb" },
		"nullable":          func(columns *fixtureRows, _ *fixtureRows) { columns.rows[2][3] = false },
		"default":           func(columns *fixtureRows, _ *fixtureRows) { columns.rows[2][4] = true },
		"variable width":    func(columns *fixtureRows, _ *fixtureRows) { columns.rows[3][2] = -1 },
		"wrong content":     func(columns *fixtureRows, _ *fixtureRows) { columns.rows[1][1] = "bytea" },
		"extra column": func(columns *fixtureRows, _ *fixtureRows) {
			columns.rows = append(columns.rows, []any{"projection", "text", -1, true, false, 5})
		},
		"missing column":    func(columns *fixtureRows, _ *fixtureRows) { columns.rows = columns.rows[:3] },
		"no primary":        func(_ *fixtureRows, primary *fixtureRows) { primary.rows = nil },
		"composite primary": func(_ *fixtureRows, primary *fixtureRows) { primary.rows[0][0] = []int16{1, 2} },
		"wrong primary":     func(_ *fixtureRows, primary *fixtureRows) { primary.rows[0][0] = []int16{2} },
		"scan failure":      func(columns *fixtureRows, _ *fixtureRows) { columns.scanErr = errors.New("native scan") },
		"source failure":    func(columns *fixtureRows, _ *fixtureRows) { columns.readErr = errors.New("native read") },
		"primary failure":   func(_ *fixtureRows, primary *fixtureRows) { primary.readErr = errors.New("native primary read") },
	} {
		t.Run(name, func(t *testing.T) {
			columns := &fixtureRows{rows: currentColumns()}
			primary := &fixtureRows{rows: [][]any{{[]int16{1}}}}
			alter(columns, primary)
			query := fixtureQuery{query: func(statement string) (pgx.Rows, error) {
				if strings.Contains(statement, "pg_constraint") {
					return primary, nil
				}
				return columns, nil
			}}
			store := Store{metadataName: "facts", fullTable: `"scope"."documents"`}
			width, err := store.readSchema(t.Context(), query)
			if name == "current" {
				if err != nil || width != 2 {
					t.Fatalf("native schema: width=%d error=%v", width, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid native schema %s accepted", name)
			}
		})
	}
	failure := errors.New("native unavailable")
	query := fixtureQuery{query: func(string) (pgx.Rows, error) { return nil, failure }}
	if _, err := (&Store{}).readSchema(t.Context(), query); !errors.Is(err, failure) {
		t.Fatalf("native query error lost: %v", err)
	}
}

func TestNativeSourceValidatedBeforeSelection(t *testing.T) {
	for name, alter := range map[string]func(*fixtureRows){
		"current":      func(*fixtureRows) {},
		"empty ID":     func(rows *fixtureRows) { rows.rows[0][0] = []byte{} },
		"empty text":   func(rows *fixtureRows) { rows.rows[0][1] = "" },
		"bad metadata": func(rows *fixtureRows) { rows.rows[0][2] = []byte("[]") },
		"bad width":    func(rows *fixtureRows) { rows.rows[0][3] = pgvec.NewVector([]float32{1}) },
		"bad float":    func(rows *fixtureRows) { rows.rows[0][3] = pgvec.NewVector([]float32{0, 0}) },
		"scan failure": func(rows *fixtureRows) { rows.scanErr = errors.New("native scan") },
		"read failure": func(rows *fixtureRows) { rows.readErr = errors.New("native read") },
	} {
		t.Run(name, func(t *testing.T) {
			rows := &fixtureRows{rows: [][]any{{[]byte("one"), "text", []byte("null"), pgvec.NewVector([]float32{1, 0})}}}
			alter(rows)
			transaction := &fixtureTransaction{query: func(statement string) (pgx.Rows, error) {
				if !strings.HasSuffix(statement, " FOR UPDATE") {
					t.Error("source is not locked")
				}
				return rows, nil
			}}
			store := Store{distanceMetric: DistanceCosine, metadataColumn: "facts", fullTable: "documents"}
			docs, err := store.readSource(t.Context(), transaction, 2, true)
			if name == "current" {
				if err != nil || len(docs) != 1 || docs[0].Metadata != nil {
					t.Fatalf("native source: docs=%v error=%v", docs, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid source %s accepted", name)
			}
		})
	}
}

func TestTransactionFinalizationPreservesFailures(t *testing.T) {
	failure := errors.New("native failure")
	for name, operationErr := range map[string]error{"success": nil, "failure": failure} {
		t.Run(name, func(t *testing.T) {
			transaction := &fixtureTransaction{rollbackErr: pgx.ErrTxClosed}
			store := Store{}
			err := operationErr
			store.finishTransaction(t.Context(), transaction, &err)
			expected := 0
			if operationErr == nil {
				expected = 1
			}
			if transaction.commits != expected || transaction.rollbacks != 1 || !errors.Is(err, operationErr) {
				t.Fatalf("transaction: commits=%d rollbacks=%d error=%v", transaction.commits, transaction.rollbacks, err)
			}
		})
	}
	transaction := &fixtureTransaction{commitErr: failure, rollbackErr: errors.New("rollback failure")}
	var err error
	(&Store{}).finishTransaction(t.Context(), transaction, &err)
	if !errors.Is(err, failure) || !errors.Is(err, transaction.rollbackErr) {
		t.Fatalf("finalization cause lost: %v", err)
	}
}
