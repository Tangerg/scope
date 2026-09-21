package clickhouse

import (
	"context"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type tableSchemaConnection struct {
	Connection
	row *tableSchemaRow
}

func (t *tableSchemaConnection) Query(context.Context, string, ...any) (driver.Rows, error) {
	return t.row, nil
}

type tableSchemaRow struct {
	driver.Rows
	engine, sorting, partition string
	read, closed               bool
}

func (t *tableSchemaRow) Next() bool {
	if t.read {
		return false
	}
	t.read = true
	return true
}
func (t *tableSchemaRow) Scan(values ...any) error {
	*values[0].(*string) = t.engine
	*values[1].(*string) = t.sorting
	*values[2].(*string) = t.partition
	return nil
}
func (*tableSchemaRow) Err() error     { return nil }
func (t *tableSchemaRow) Close() error { t.closed = true; return nil }

func TestCurrentRecordSchemaIsRequired(t *testing.T) {
	for _, sample := range []struct {
		engine, sorting, partition string
		valid                      bool
	}{
		{"ReplacingMergeTree ORDER BY id SETTINGS index_granularity = 8192", "id", "", true},
		{"ReplacingMergeTree() ORDER BY id", "id", "", true},
		{"MergeTree ORDER BY id", "id", "", false},
		{"ReplacingMergeTree(version) ORDER BY id", "id", "", false},
		{"ReplacingMergeTree ORDER BY (id, tenant)", "id, tenant", "", false},
		{"ReplacingMergeTree PARTITION BY tenant ORDER BY id", "id", "tenant", false},
	} {
		t.Run(sample.engine, func(t *testing.T) {
			row := &tableSchemaRow{engine: sample.engine, sorting: sample.sorting, partition: sample.partition}
			store := &Store{conn: &tableSchemaConnection{row: row}, idColumn: "id", tableName: "documents", fullTable: "documents"}
			err := store.validateTable(t.Context())
			if (err == nil) != sample.valid {
				t.Fatalf("schema validation = %v; valid=%t", err, sample.valid)
			}
			if !row.closed {
				t.Fatal("schema rows were not closed")
			}
		})
	}
}
