package clickhouse

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCurrentRecordSchemaIsRequired(t *testing.T) {
	for _, sample := range []struct {
		engine, sorting, partition string
		valid                      bool
	}{
		{"ReplacingMergeTree ORDER BY id SETTINGS index_granularity = 8192", "id", "", true},
		{"ReplacingMergeTree() ORDER BY id", "id", "", true},
		{"ReplacingMergeTree ORDER BY (id)", "(id)", "", true},
		{"ReplacingMergeTree ORDER BY ((id))", "((id))", "", true},
		{"MergeTree ORDER BY id", "id", "", false},
		{"ReplacingMergeTree(version) ORDER BY id", "id", "", false},
		{"ReplacingMergeTree ORDER BY (id, tenant)", "id, tenant", "", false},
		{"ReplacingMergeTree ORDER BY (id, tenant)", "(id, tenant)", "", false},
		{"ReplacingMergeTree ORDER BY lower(id)", "lower(id)", "", false},
		{"ReplacingMergeTree ORDER BY (lower(id))", "(lower(id))", "", false},
		{"ReplacingMergeTree PARTITION BY tenant ORDER BY id", "id", "tenant", false},
	} {
		t.Run(sample.engine, func(t *testing.T) {
			store, mock := mockStore(t)
			mock.ExpectQuery("SELECT engine_full").WithArgs("scope", "documents").WillReturnRows(sqlmock.NewRows([]string{"engine", "sorting", "partition", "ddl"}).AddRow(sample.engine, sample.sorting, sample.partition, currentDDL))
			if sample.valid {
				expectColumns(mock, "String", "")
			}
			err := store.validateTable(t.Context())
			if (err == nil) != sample.valid {
				t.Fatalf("schema validation = %v; valid=%t", err, sample.valid)
			}
		})
	}
}

func TestStrictMetadataColumnAndProducerConstraints(t *testing.T) {
	for _, sample := range []struct{ name, metadataType, defaultKind, ddl string }{
		{"map metadata", "Map(String, String)", "", currentDDL},
		{"native JSON metadata", "JSON", "", currentDDL},
		{"nullable metadata", "Nullable(String)", "", currentDDL},
		{"generated metadata", "String", "MATERIALIZED", currentDDL},
		{"missing width constraint", "String", "", "CONSTRAINT vec_finite CHECK arrayAll(x -> isFinite(x), embedding)"},
		{"missing finite constraint", "String", "", "CONSTRAINT vec_len CHECK length(embedding) = 2,"},
		{"constraints inside a comment", "String", "", "    `metadata` String COMMENT 'CONSTRAINT vec_len CHECK length(embedding) = 2,\\nCONSTRAINT vec_finite CHECK arrayAll(x -> isFinite(x), embedding)\\n'\n"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store, mock := mockStore(t)
			mock.ExpectQuery("SELECT engine_full").WillReturnRows(sqlmock.NewRows([]string{"engine", "sorting", "partition", "ddl"}).AddRow("ReplacingMergeTree ORDER BY id", "id", "", sample.ddl))
			if sample.ddl == currentDDL {
				expectColumns(mock, sample.metadataType, sample.defaultKind)
			}
			if err := store.validateTable(t.Context()); err == nil {
				t.Fatal("accepted a competing or incomplete storage representation")
			}
		})
	}
}

const currentDDL = "CONSTRAINT vec_len CHECK length(embedding) = 2,\nCONSTRAINT vec_finite CHECK arrayAll(x -> isFinite(x), embedding)\n"

func expectColumns(mock sqlmock.Sqlmock, metadataType, defaultKind string) {
	mock.ExpectQuery("SELECT name, type, default_kind").WillReturnRows(sqlmock.NewRows([]string{"name", "type", "default_kind"}).AddRow("id", "String", "").AddRow("content", "String", "").AddRow("metadata", metadataType, defaultKind).AddRow("embedding", "Array(Float32)", "")).RowsWillBeClosed()
}
