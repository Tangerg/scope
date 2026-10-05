package tidb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

type resultConnector struct{ rows *resultRows }

func (r resultConnector) Connect(context.Context) (driver.Conn, error) {
	return &resultConnection{rows: r.rows}, nil
}
func (r resultConnector) Driver() driver.Driver { return resultDriver{} }

type resultDriver struct{}

func (r resultDriver) Open(string) (driver.Conn, error) { return nil, errors.New("connector required") }

type resultConnection struct{ rows *resultRows }

func (r *resultConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected preparation")
}
func (r *resultConnection) Close() error { return nil }
func (r *resultConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (r *resultConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return r.rows, nil
}

type resultRows struct {
	values   [][]driver.Value
	position int
	closed   bool
}

func (r *resultRows) Columns() []string { return []string{"id", "content", "metadata", "distance"} }
func (r *resultRows) Close() error      { r.closed = true; return nil }
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
				store := offlineStore(t, db, embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
					return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
				}))
				response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 2, MinScore: 0.9}})
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
	store := offlineStore(t, db, embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
	}))
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 2, MinScore: 0.9}})
	if err != nil || response == nil || len(response.Results) != 0 || !rows.closed {
		t.Fatalf("Search: response=%v error=%v closed=%t", response, err, rows.closed)
	}
}
