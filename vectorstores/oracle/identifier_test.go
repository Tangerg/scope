package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type identifierConnector struct{ connection *identifierConnection }

func (i identifierConnector) Connect(context.Context) (driver.Conn, error) { return i.connection, nil }
func (i identifierConnector) Driver() driver.Driver                        { return resultDriver{} }

type identifierConnection struct {
	t          *testing.T
	statements []string
}

func (i *identifierConnection) Prepare(statement string) (driver.Stmt, error) {
	i.statements = append(i.statements, statement)
	return &writeStmt{}, nil
}

func (i *identifierConnection) Close() error { return nil }

func (i *identifierConnection) Begin() (driver.Tx, error) {
	return &transactionOutcome{fixture: &transactionFixture{}}, nil
}

func (i *identifierConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return i.Begin()
}

func (i *identifierConnection) CheckNamedValue(*driver.NamedValue) error { return nil }

func (i *identifierConnection) ExecContext(_ context.Context, statement string, _ []driver.NamedValue) (driver.Result, error) {
	i.statements = append(i.statements, statement)
	return driver.RowsAffected(1), nil
}

func (i *identifierConnection) QueryContext(_ context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	rows := func(columns []string, values ...driver.Value) driver.Rows {
		return &resultRows{cols: columns, values: [][]driver.Value{values}}
	}
	switch {
	case strings.Contains(statement, "NLS_DATABASE_PARAMETERS"):
		return rows([]string{"VALUE"}, "AL32UTF8"), nil
	case strings.Contains(statement, "SELECT DATA_TYPE, DATA_LENGTH, NULLABLE"):
		var field, dataType string
		var length int64
		switch args[2].Value {
		case "PRIMARY":
			field, dataType, length = "PRIMARY", "RAW", 2000
		case "SELECT":
			field, dataType = "SELECT", "BLOB"
		case "FROM":
			field, dataType = "FROM", "CLOB"
		default:
			return nil, fmt.Errorf("unexpected catalog field: %v", args[2].Value)
		}
		i.requireCatalogArguments(args, "SCHEMA", "TABLE", field)
		return rows([]string{"DATA_TYPE", "DATA_LENGTH", "NULLABLE"}, dataType, length, "N"), nil
	case strings.Contains(statement, "SELECT c.CONSTRAINT_TYPE"):
		i.requireCatalogArguments(args, "SCHEMA", "TABLE")
		return rows([]string{"TYPE", "STATUS", "VALIDATED", "COLUMN", "POSITION"}, "P", "ENABLED", "VALIDATED", "PRIMARY", int64(1)), nil
	case strings.Contains(statement, "SELECT cc.COLUMN_NAME, cc.COLUMN_POSITION"):
		i.requireCatalogArguments(args, "SCHEMA", "TABLE")
		return rows([]string{"COLUMN", "POSITION"}, "PRIMARY", int64(1)), nil
	case strings.Contains(statement, "SELECT VECTOR_INFO"):
		i.requireCatalogArguments(args, "SCHEMA", "TABLE", "WHERE")
		return rows([]string{"VECTOR_INFO"}, "VECTOR(2,FLOAT32)"), nil
	case strings.Contains(statement, "SELECT COUNT(*) FROM ALL_CONSTRAINTS"):
		i.requireCatalogArguments(args, "SCHEMA", "TABLE", strings.ToUpper(fixtureSpace.StorageKey()), "SV_")
		return rows([]string{"COUNT"}, int64(1)), nil
	}
	i.statements = append(i.statements, statement)
	switch {
	case strings.Contains(statement, "SELECT CASE WHEN COUNT(*)"):
		return rows([]string{"allowed"}, int64(1)), nil
	case strings.Contains(statement, " AS distance "):
		return rows([]string{"id", "content", "metadata", "distance"}, []byte("one"), "content", []byte(`{"tenant":"alpha"}`), float64(0)), nil
	default:
		return rows([]string{"id", "metadata"}, []byte("one"), []byte(`{"tenant":"alpha"}`)), nil
	}
}

func (i *identifierConnection) requireCatalogArguments(args []driver.NamedValue, want ...string) {
	i.t.Helper()
	var got []string
	for _, arg := range args {
		value, ok := arg.Value.(string)
		if !ok {
			i.t.Fatalf("catalog argument is not a native name: %T", arg.Value)
		}
		got = append(got, value)
	}
	if !slices.Equal(got, want) {
		i.t.Fatalf("catalog names = %q, want %q", got, want)
	}
}

func (i *identifierConnection) assertStatements(want ...string) {
	i.t.Helper()
	got := make([]string, len(i.statements))
	for index, statement := range i.statements {
		got[index] = strings.Join(strings.Fields(statement), " ")
	}
	if !slices.Equal(got, want) {
		i.t.Fatalf("SQL statements:\n%q\nwant:\n%q", got, want)
	}
	i.statements = nil
}

func TestConfiguredIdentifiersKeepCatalogAndSQLIdentity(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		t.Run(fmt.Sprintf("initialize=%v", initialize), func(t *testing.T) {
			i := &identifierConnection{t: t}
			db := sql.OpenDB(identifierConnector{connection: i})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			config := StoreConfig{DB: db, SchemaName: "schema", TableName: "table", IDColumn: "primary", ContentColumn: "from", MetadataColumn: "select", EmbeddingColumn: "where", Space: fixtureSpace, CreateDimensions: creationWidth(initialize)}
			store, err := NewStore(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			if initialize {
				i.assertStatements(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "SCHEMA"."TABLE" ( "PRIMARY" RAW(2000) NOT NULL PRIMARY KEY, "FROM" CLOB NOT NULL, "SELECT" BLOB NOT NULL, "WHERE" VECTOR(2, FLOAT32) NOT NULL, CONSTRAINT %s CHECK (1=1) )`, `"`+strings.ToUpper(fixtureSpace.StorageKey())+`"`))
			} else {
				i.assertStatements()
			}
			const (
				upsertSQL         = "MERGE INTO \"SCHEMA\".\"TABLE\" tgt USING (SELECT :1 AS id, :2 AS content, :3 AS metadata, TO_VECTOR(:4, *, FLOAT32) AS embedding FROM dual) src ON (tgt.\"PRIMARY\" = src.id) WHEN MATCHED THEN UPDATE SET tgt.\"FROM\" = src.content, tgt.\"SELECT\" = src.metadata, tgt.\"WHERE\" = src.embedding WHEN NOT MATCHED THEN INSERT (tgt.\"PRIMARY\", tgt.\"FROM\", tgt.\"SELECT\", tgt.\"WHERE\") VALUES (src.id, src.content, src.metadata, src.embedding)"
				searchSQL         = "SELECT \"PRIMARY\", \"FROM\", \"SELECT\", VECTOR_DISTANCE(\"WHERE\", TO_VECTOR(:1, *, FLOAT32), COSINE) AS distance FROM \"SCHEMA\".\"TABLE\" ORDER BY distance ASC, \"PRIMARY\" ASC FETCH FIRST :2 ROWS ONLY"
				selectedSQL       = "SELECT \"PRIMARY\", \"FROM\", \"SELECT\", VECTOR_DISTANCE(\"WHERE\", TO_VECTOR(:1, *, FLOAT32), COSINE) AS distance FROM \"SCHEMA\".\"TABLE\" WHERE \"PRIMARY\" IN (:2) ORDER BY distance ASC, \"PRIMARY\" ASC FETCH FIRST :3 ROWS ONLY"
				budgetSQL         = "SELECT CASE WHEN COUNT(*)<=:1 AND 2*COALESCE(SUM(n),0)<=:2 THEN 1 ELSE 0 END FROM (SELECT UTL_RAW.LENGTH(\"PRIMARY\")+COALESCE(DBMS_LOB.GETLENGTH(\"SELECT\"),0) n FROM \"SCHEMA\".\"TABLE\" FETCH FIRST :3 ROWS ONLY)"
				pageSQL           = "SELECT \"PRIMARY\", \"SELECT\" FROM \"SCHEMA\".\"TABLE\" ORDER BY \"PRIMARY\" ASC FETCH FIRST :1 ROWS ONLY"
				deleteSQL         = "DELETE FROM \"SCHEMA\".\"TABLE\" WHERE \"PRIMARY\" IN (:1)"
				deleteMatchingSQL = "DELETE FROM \"SCHEMA\".\"TABLE\" WHERE \"PRIMARY\" IN (:1)"
			)
			if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "content"}})); err != nil {
				t.Fatal(err)
			}
			i.assertStatements(upsertSQL)
			query := &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: 1}}
			if _, err := store.Query(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			i.assertStatements(searchSQL)
			query.Options.Filter = filter.EQ("tenant", "alpha")
			if _, err := store.QueryByScan(t.Context(), query, fixtureBudget()); err != nil {
				t.Fatal(err)
			}
			i.assertStatements("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE", budgetSQL, pageSQL, pageSQL, selectedSQL)
			if err := store.DeleteMatching(t.Context(), query.Options.Filter, fixtureBudget()); err != nil {
				t.Fatal(err)
			}
			i.assertStatements("SET TRANSACTION ISOLATION LEVEL READ COMMITTED", `LOCK TABLE "SCHEMA"."TABLE" IN EXCLUSIVE MODE NOWAIT`, budgetSQL, pageSQL, pageSQL, deleteMatchingSQL)
			if err := store.DeleteIDs(t.Context(), []string{"one"}); err != nil {
				t.Fatal(err)
			}
			i.assertStatements(deleteSQL)
		})
	}
}
