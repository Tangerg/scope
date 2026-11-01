package tidb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
)

type resultConnector struct {
	rows *resultRows
	stmt driver.Stmt
}

func (r resultConnector) Connect(context.Context) (driver.Conn, error) {
	return &resultConnection{rows: r.rows, stmt: r.stmt}, nil
}
func (r resultConnector) Driver() driver.Driver { return resultDriver{} }

type resultDriver struct{}

func (r resultDriver) Open(string) (driver.Conn, error) { return nil, errors.New("connector required") }

type resultConnection struct {
	rows *resultRows
	stmt driver.Stmt
}

func (r *resultConnection) Prepare(string) (driver.Stmt, error) {
	if r.stmt != nil {
		return r.stmt, nil
	}
	return nil, errors.New("unexpected preparation")
}
func (r *resultConnection) Close() error { return nil }
func (r *resultConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (r *resultConnection) QueryContext(_ context.Context, stmt string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(stmt, "SELECT VECTOR_INFO") {
		return &resultRows{cols: []string{"VECTOR_INFO"}, values: [][]driver.Value{{"VECTOR(2,FLOAT32)"}}}, nil
	}
	if strings.Contains(stmt, "SELECT COUNT(*) FROM ALL_CONSTRAINTS") {
		return &resultRows{cols: []string{"COUNT"}, values: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(stmt, "TABLE_COMMENT") {
		return &resultRows{cols: []string{"TABLE_COMMENT", "COLUMN_TYPE"}, values: [][]driver.Value{{fixtureSpace.StorageKey(), "vector(2)"}}}, nil
	}

	return r.rows, nil
}

type resultRows struct {
	cols     []string
	values   [][]driver.Value
	position int
	closed   bool
}

func (r *resultRows) Columns() []string {
	if r.cols != nil {
		return r.cols
	}
	return []string{"id", "content", "metadata", "distance"}
}
func (r *resultRows) Close() error { r.closed = true; return nil }
func (r *resultRows) Next(destinations []driver.Value) error {
	if r.position == len(r.values) {
		return io.EOF
	}
	copy(destinations, r.values[r.position])
	r.position++
	return nil
}

func TestSearchValidatesRowsBeforeMinScore(t *testing.T) {
	for _, test := range []struct {
		name  string
		id    driver.Value
		text  driver.Value
		facts driver.Value
	}{
		{"missing ID", []byte{}, "text", []byte("null")},
		{"missing text", []byte("bad"), nil, []byte("null")},
		{"empty text", []byte("bad"), "", []byte("null")},
		{"invalid UTF-8 text", []byte("bad"), string([]byte{0xff}), []byte("null")},
		{"array metadata", []byte("bad"), "text", []byte("[]")},
		{"malformed metadata", []byte("bad"), "text", []byte("{")},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, distance := range []float64{0, 2} {
				rows := &resultRows{values: [][]driver.Value{
					{[]byte("valid"), "valid text", []byte("{}"), float64(0)},
					{test.id, test.text, test.facts, distance},
				}}
				db := sql.OpenDB(resultConnector{rows: rows})
				t.Cleanup(func() {
					if err := db.Close(); err != nil {
						t.Error(err)
					}
				})
				store := offlineStore(t, db)
				response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 2, MinScore: 0.9}})
				if err == nil || response != nil || !rows.closed {
					t.Fatalf("Search: response=%v error=%v closed=%t distance=%v", response, err, rows.closed, distance)
				}
			}
		})
	}
}

func TestSearchOmitsValidLowScoreRows(t *testing.T) {
	rows := &resultRows{values: [][]driver.Value{
		{[]byte("one"), "text", []byte("null"), float64(2)},
		{[]byte("two"), "text", []byte("{}"), float64(2)},
	}}
	db := sql.OpenDB(resultConnector{rows: rows})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := offlineStore(t, db)
	response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 2, MinScore: 0.9}})
	if err != nil || response == nil || len(response.Results) != 0 || !rows.closed {
		t.Fatalf("Search: response=%v error=%v closed=%t", response, err, rows.closed)
	}
}

func (r *resultConnection) CheckNamedValue(*driver.NamedValue) error { return nil }

type writeStmt struct {
	ids  []string
	fail error
}

func (w *writeStmt) Close() error  { return nil }
func (w *writeStmt) NumInput() int { return 4 }
func (w *writeStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("unexpected query")
}
func (w *writeStmt) Exec(args []driver.Value) (driver.Result, error) {
	id := string(args[0].([]byte))
	w.ids = append(w.ids, id)
	if id == "two" {
		return nil, w.fail
	}
	return driver.RowsAffected(1), nil
}
func TestSingleRecordWritesRetainConfirmedAndUnknownIDs(t *testing.T) {
	stmt := &writeStmt{fail: errors.New("response lost")}
	db := sql.OpenDB(resultConnector{rows: &resultRows{}, stmt: stmt})
	defer db.Close()
	s := offlineStore(t, db)
	batch := fixtureBatch([]*document.Document{{ID: "one", Text: "one"}, {ID: "two", Text: "two"}, {ID: "three", Text: "three"}})
	err := s.Upsert(t.Context(), batch)
	w, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok || !errors.Is(err, stmt.fail) || !slices.Equal(stmt.ids, []string{"one", "two"}) || !slices.Equal(w.Acknowledged, []string{"one"}) || !slices.Equal(w.Unconfirmed, []string{"two"}) || !slices.Equal(w.Unattempted, []string{"three"}) {
		t.Fatalf("calls=%v evidence=%v err=%v", stmt.ids, w, err)
	}
}
