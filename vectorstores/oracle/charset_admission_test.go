package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
)

type charsetSchemaConnector struct{ connection *charsetSchemaConnection }

func (c charsetSchemaConnector) Driver() driver.Driver { return resultDriver{} }
func (c charsetSchemaConnector) Connect(context.Context) (driver.Conn, error) {
	return c.connection, nil
}

type charsetSchemaConnection struct {
	resultConnection
	characterSet    driver.Value
	contentType     string
	contentNullable string
	charsetQueries  int
}

func (c *charsetSchemaConnection) QueryContext(ctx context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(statement, "NLS_DATABASE_PARAMETERS"):
		c.charsetQueries++
		return &resultRows{cols: []string{"VALUE"}, values: [][]driver.Value{{c.characterSet}}}, nil
	case strings.Contains(statement, "SELECT DATA_TYPE, DATA_LENGTH, NULLABLE"):
		field := strings.ToUpper(args[2].Value.(string))
		var schema []driver.Value
		switch field {
		case "ID":
			schema = []driver.Value{"RAW", int64(2000), "N"}
		case "METADATA":
			schema = []driver.Value{"BLOB", int64(0), "N"}
		case "CONTENT":
			schema = []driver.Value{c.contentType, int64(0), c.contentNullable}
		default:
			return nil, fmt.Errorf("unexpected schema field %q", field)
		}
		return &resultRows{cols: []string{"DATA_TYPE", "DATA_LENGTH", "NULLABLE"}, values: [][]driver.Value{schema}}, nil
	case strings.Contains(statement, "SELECT c.CONSTRAINT_TYPE"):
		return &resultRows{cols: []string{"TYPE", "STATUS", "VALIDATED", "COLUMN", "POSITION"}, values: [][]driver.Value{{"P", "ENABLED", "VALIDATED", "ID", int64(1)}}}, nil
	case strings.Contains(statement, "SELECT cc.COLUMN_NAME, cc.COLUMN_POSITION"):
		return &resultRows{cols: []string{"COLUMN", "POSITION"}, values: [][]driver.Value{{"ID", int64(1)}}}, nil
	case strings.Contains(statement, "SELECT VECTOR_INFO"), strings.Contains(statement, "SELECT COUNT(*) FROM ALL_CONSTRAINTS"):
		return c.resultConnection.QueryContext(ctx, statement, args)
	default:
		return nil, fmt.Errorf("unexpected query %q", statement)
	}
}

func TestContentBindingRequiresLosslessDatabaseCharset(t *testing.T) {
	for _, characterSet := range []driver.Value{"WE8MSWIN1252", "UTF8", "", nil} {
		t.Run(fmt.Sprint(characterSet), func(t *testing.T) {
			statement := &writeStmt{}
			connection := &charsetSchemaConnection{resultConnection: resultConnection{stmt: statement}, characterSet: characterSet, contentType: "CLOB", contentNullable: "N"}
			db := sql.OpenDB(charsetSchemaConnector{connection: connection})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			store, err := NewStore(t.Context(), StoreConfig{DB: db, SchemaName: "APP", Space: fixtureSpace})
			if store != nil || err == nil || connection.charsetQueries != 1 {
				t.Fatalf("store=%v error=%v charset queries=%d", store, err, connection.charsetQueries)
			}
			store = offlineStore(t, db)
			docs := []*document.Document{{ID: "one", Text: "证据 😀"}}
			if err := store.CheckDocuments(t.Context(), fixtureSpace, docs); err == nil {
				t.Fatal("lossy CLOB admission succeeded")
			}
			if err := store.Upsert(t.Context(), fixtureBatch(docs)); err == nil || len(statement.ids) != 0 {
				t.Fatalf("upsert=%v writes=%v", err, statement.ids)
			}
		})
	}
}

func TestContentBindingAcceptsUnicodeAndRechecksCharset(t *testing.T) {
	statement := &writeStmt{}
	connection := &charsetSchemaConnection{resultConnection: resultConnection{stmt: statement}, characterSet: "AL32UTF8", contentType: "CLOB", contentNullable: "N"}
	db := sql.OpenDB(charsetSchemaConnector{connection: connection})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store, err := NewStore(t.Context(), StoreConfig{DB: db, SchemaName: "APP", Space: fixtureSpace})
	if err != nil {
		t.Fatal(err)
	}
	docs := []*document.Document{{ID: "one", Text: "证据 😀"}}
	if err := store.CheckDocuments(t.Context(), fixtureSpace, docs); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil || len(statement.ids) != 1 {
		t.Fatalf("upsert=%v writes=%v", err, statement.ids)
	}
	connection.characterSet = "WE8MSWIN1252"
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err == nil || len(statement.ids) != 1 {
		t.Fatalf("changed charset admitted: upsert=%v writes=%v", err, statement.ids)
	}
}

func TestContentBindingRequiresNonNullClob(t *testing.T) {
	for _, schema := range []struct{ dataType, nullable string }{{"VARCHAR2", "N"}, {"NCLOB", "N"}, {"CLOB", "Y"}} {
		t.Run(schema.dataType+schema.nullable, func(t *testing.T) {
			connection := &charsetSchemaConnection{characterSet: "AL32UTF8", contentType: schema.dataType, contentNullable: schema.nullable}
			db := sql.OpenDB(charsetSchemaConnector{connection: connection})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			store, err := NewStore(t.Context(), StoreConfig{DB: db, SchemaName: "APP", Space: fixtureSpace})
			if store != nil || err == nil {
				t.Fatalf("store=%v error=%v", store, err)
			}
		})
	}
}
