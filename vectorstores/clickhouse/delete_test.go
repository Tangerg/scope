package clickhouse

import (
	"context"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// recordingConnection captures the statements a delete issues. The embedded
// interface stays nil so reaching any other operation fails the test.
type recordingConnection struct {
	Connection
	statements []string
	args       [][]any
}

func (r *recordingConnection) Exec(_ context.Context, query string, args ...any) error {
	r.statements = append(r.statements, query)
	r.args = append(r.args, args)
	return nil
}

func deleteStore(connection Connection) *Store {
	return &Store{
		conn:           connection,
		fullTable:      "scope.vector_store",
		idColumn:       "id",
		metadataColumn: "metadata",
	}
}

// `ALTER TABLE ... DELETE` records a mutation and returns while it still runs,
// so neither delete path may issue one: a queued mutation cannot carry the
// contract that the rows are gone.
func TestDeletesIssueLightweightStatements(t *testing.T) {
	t.Parallel()

	expression, err := filter.Parse(`tenant == 'acme'`)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name string
		call func(*Store) error
		want string
	}{
		{
			name: "by filter",
			call: func(store *Store) error { return store.DeleteWhere(t.Context(), expression) },
			want: `DELETE FROM scope.vector_store WHERE (mapContains(metadata, 'tenant') AND metadata['tenant'] = ?)`,
		},
		{
			name: "by ids",
			call: func(store *Store) error { return store.DeleteIDs(t.Context(), []string{"one", "two"}) },
			want: `DELETE FROM scope.vector_store WHERE id IN (?, ?)`,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			connection := &recordingConnection{}
			if err := sample.call(deleteStore(connection)); err != nil {
				t.Fatalf("delete = %v, want nil", err)
			}
			if len(connection.statements) != 1 {
				t.Fatalf("statements = %v, want exactly one", connection.statements)
			}
			statement := connection.statements[0]
			if statement != sample.want {
				t.Fatalf("statement = %q, want %q", statement, sample.want)
			}
			if strings.Contains(statement, "ALTER TABLE") {
				t.Fatalf("statement %q deletes through an asynchronous mutation", statement)
			}
		})
	}
}

// A ClickHouse identifier can only reach the statement through configuration,
// so the values a filter selects on stay bound rather than interpolated.
func TestDeleteWhereBindsFilterValues(t *testing.T) {
	t.Parallel()

	expression, err := filter.Parse(`tenant == 'acme' and year > 2020`)
	if err != nil {
		t.Fatal(err)
	}
	connection := &recordingConnection{}
	if err := deleteStore(connection).DeleteWhere(t.Context(), expression); err != nil {
		t.Fatalf("DeleteWhere() = %v, want nil", err)
	}
	if got := connection.args[0]; len(got) != 2 {
		t.Fatalf("bound arguments = %v, want two", got)
	}
	if got := connection.args[0][0]; got != "acme" {
		t.Fatalf("bound arguments[0] = %v, want %q", got, "acme")
	}
}

// An empty filter must not become an unfiltered delete.
func TestDeleteRefusesToTouchEveryRow(t *testing.T) {
	t.Parallel()

	connection := &recordingConnection{}
	if err := deleteStore(connection).DeleteWhere(t.Context(), nil); err == nil {
		t.Fatal("DeleteWhere(nil) = nil, want an error")
	}
	if err := deleteStore(connection).DeleteIDs(t.Context(), nil); err != nil {
		t.Fatalf("DeleteIDs(nil) = %v, want nil", err)
	}
	if len(connection.statements) != 0 {
		t.Fatalf("statements = %v, want none", connection.statements)
	}
}

var _ Connection = (driver.Conn)(nil)
