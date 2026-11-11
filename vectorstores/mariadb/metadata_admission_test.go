package mariadb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type metadataSchemaConnector struct{ connection *metadataSchemaConnection }

func (m metadataSchemaConnector) Connect(context.Context) (driver.Conn, error) {
	return m.connection, nil
}

func (m metadataSchemaConnector) Driver() driver.Driver { return resultDriver{} }

type metadataSchemaConnection struct {
	*resultConnection
	metadataInspections int
}

func (m *metadataSchemaConnection) QueryContext(ctx context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(statement, "SELECT c.DATA_TYPE"):
		return &resultRows{cols: []string{"DATA_TYPE", "CHARACTER_MAXIMUM_LENGTH", "IS_NULLABLE", "ENGINE"}, values: [][]driver.Value{{"varbinary", int64(3072), "NO", "InnoDB"}}}, nil
	case strings.Contains(statement, "information_schema.STATISTICS"):
		return &resultRows{cols: []string{"INDEX_NAME", "COLUMN_NAME", "SEQ_IN_INDEX", "SUB_PART"}, values: [][]driver.Value{{"PRIMARY", "id", int64(1), nil}}}, nil
	case strings.Contains(statement, "TABLE_COMMENT"):
		if len(args) != 5 || args[4].Value != "metadata" {
			return nil, fmt.Errorf("metadata column omitted from native schema query: %v", args)
		}
		m.metadataInspections++
		return m.resultConnection.QueryContext(ctx, statement, args)
	default:
		return nil, fmt.Errorf("unexpected schema query %s", statement)
	}
}

func TestMetadataBindingRejectsLossyNativeColumns(t *testing.T) {
	for _, test := range []struct {
		name     string
		dataType string
		charset  string
		capacity driver.Value
		accepted bool
	}{
		{"JSON alias", "longtext", "utf8mb4", int64(4294967295), true},
		{"bounded text", "tinytext", "utf8mb4", int64(255), true},
		{"VARCHAR is not byte bounded", "varchar", "utf8mb4", int64(16), false},
		{"non-Unicode metadata", "text", "latin1", int64(65535), false},
		{"missing capacity", "text", "utf8mb4", nil, false},
		{"zero capacity", "text", "utf8mb4", int64(0), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			statement := &writeStmt{}
			connection := &metadataSchemaConnection{resultConnection: &resultConnection{stmt: statement, binding: []driver.Value{
				fixtureSpace.StorageKey(), "vector(2)", "text", "utf8mb4", int64(65535), test.dataType, test.charset, test.capacity,
			}}}
			db := sql.OpenDB(metadataSchemaConnector{connection: connection})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			store, err := NewStore(t.Context(), StoreConfig{DB: db, SchemaName: "scope", TableName: "documents", Space: fixtureSpace})
			if connection.metadataInspections != 1 || len(statement.ids) != 0 {
				t.Fatalf("metadata inspections=%d writes=%v", connection.metadataInspections, statement.ids)
			}
			if test.accepted {
				if err != nil || store == nil {
					t.Fatalf("store=%v error=%v", store, err)
				}
			} else if err == nil || store != nil {
				t.Fatalf("incompatible metadata column admitted: store=%v error=%v", store, err)
			}
		})
	}
}

func TestMetadataAdmissionChecksFinalEncodedBytesBeforeAnyWrite(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    string
		accepted bool
	}{
		{"exact bytes", strings.Repeat("x", 243), true},
		{"one byte over", strings.Repeat("x", 244), false},
		{"UTF-8 exact bytes", strings.Repeat("界", 81), true},
		{"UTF-8 one byte over", strings.Repeat("界", 81) + "x", false},
		{"JSON expansion", strings.Repeat(`"`, 122), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			statement := &writeStmt{}
			db := sql.OpenDB(resultConnector{stmt: statement, binding: []driver.Value{
				fixtureSpace.StorageKey(), "vector(2)", "text", "utf8mb4", int64(65535), "tinytext", "utf8mb4", int64(255),
			}})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			facts := make(metadata.Map)
			if err := facts.Set("value", test.value); err != nil {
				t.Fatal(err)
			}
			docs := []*document.Document{{ID: "first", Text: "valid"}, {ID: "last", Text: "text", Metadata: facts}}
			store := offlineStore(t, db)
			admissionErr := store.CheckDocuments(t.Context(), fixtureSpace, docs)
			writeErr := store.Upsert(t.Context(), fixtureBatch(docs))
			if test.accepted {
				if admissionErr != nil || writeErr != nil || len(statement.ids) != 2 {
					t.Fatalf("admission=%v upsert=%v writes=%v", admissionErr, writeErr, statement.ids)
				}
			} else if !errors.Is(admissionErr, vectorstore.ErrInvalidDocument) || !errors.Is(writeErr, vectorstore.ErrInvalidDocument) || len(statement.ids) != 0 {
				t.Fatalf("admission=%v upsert=%v writes=%v", admissionErr, writeErr, statement.ids)
			}
		})
	}
}

func TestMetadataAdmissionRechecksNativeCapacity(t *testing.T) {
	binding := []driver.Value{fixtureSpace.StorageKey(), "vector(2)", "text", "utf8mb4", int64(65535), "longtext", "utf8mb4", int64(4294967295)}
	statement := &writeStmt{}
	db := sql.OpenDB(resultConnector{stmt: statement, binding: binding})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := offlineStore(t, db)
	facts := make(metadata.Map)
	if err := facts.Set("value", strings.Repeat("x", 244)); err != nil {
		t.Fatal(err)
	}
	docs := []*document.Document{{ID: "document", Text: "text", Metadata: facts}}
	if err := store.CheckDocuments(t.Context(), fixtureSpace, docs); err != nil {
		t.Fatal(err)
	}
	binding[5], binding[7] = "tinytext", int64(255)
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); !errors.Is(err, vectorstore.ErrInvalidDocument) || len(statement.ids) != 0 {
		t.Fatalf("upsert=%v writes=%v", err, statement.ids)
	}
}
