package mariadb

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

func (i *identifierConnection) ExecContext(_ context.Context, statement string, _ []driver.NamedValue) (driver.Result, error) {
	i.statements = append(i.statements, statement)
	return driver.RowsAffected(1), nil
}
func (i *identifierConnection) QueryContext(_ context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	rows := func(columns []string, values ...driver.Value) driver.Rows {
		return &resultRows{cols: columns, values: [][]driver.Value{values}}
	}
	switch {
	case strings.Contains(statement, "SELECT c.DATA_TYPE"):
		i.requireCatalogArguments(args, "schema", "table", "primary")
		return rows([]string{"DATA_TYPE", "CHARACTER_MAXIMUM_LENGTH", "IS_NULLABLE", "ENGINE"}, "varbinary", int64(3072), "NO", "InnoDB"), nil
	case strings.Contains(statement, "information_schema.STATISTICS"):
		i.requireCatalogArguments(args, "schema", "table")
		return rows([]string{"INDEX_NAME", "COLUMN_NAME", "SEQ_IN_INDEX", "SUB_PART"}, "PRIMARY", "primary", int64(1), nil), nil
	case strings.Contains(statement, "TABLE_COMMENT"):
		i.requireCatalogArguments(args, "schema", "table", "where", "from", "select")
		return rows([]string{"marker", "shape", "contentType", "contentCharset", "contentBytes", "metadataType", "metadataCharset", "metadataBytes"}, fixtureSpace.StorageKey(), "vector(2)", "text", "utf8mb4", int64(65535), "longtext", "utf8mb4", int64(4294967295)), nil
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
			config := StoreConfig{DB: db, SchemaName: "schema", TableName: "table", IDColumn: "primary", ContentColumn: "from", MetadataColumn: "select", EmbeddingColumn: "where", Space: fixtureSpace, Dimensions: 2, InitializeSchema: initialize}
			store, err := NewStore(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			if initialize {
				i.assertStatements("CREATE SCHEMA IF NOT EXISTS `schema`", fmt.Sprintf("CREATE TABLE IF NOT EXISTS `schema`.`table` ( `primary` VARBINARY(3072) NOT NULL PRIMARY KEY, `from` TEXT, `select` JSON, `where` VECTOR(2) NOT NULL ) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COMMENT='%s'", fixtureSpace.StorageKey()))
			} else {
				i.assertStatements()
			}
			const (
				upsertSQL         = "INSERT INTO `schema`.`table` (`primary`, `from`, `select`, `where`) VALUES (?, ?, ?, VEC_FromText(?)) ON DUPLICATE KEY UPDATE `from` = VALUES(`from`), `select` = VALUES(`select`), `where` = VALUES(`where`)"
				searchSQL         = "SELECT `primary`, `from`, `select`, vec_distance_cosine(`where`, VEC_FromText(?)) AS distance FROM `schema`.`table` FORCE INDEX(PRIMARY) WHERE 1=1 ORDER BY distance ASC, `primary` ASC LIMIT ?"
				selectedSQL       = "SELECT `primary`, `from`, `select`, vec_distance_cosine(`where`, VEC_FromText(?)) AS distance FROM `schema`.`table` FORCE INDEX(PRIMARY) WHERE 1=1 AND `primary` IN (?) ORDER BY distance ASC, `primary` ASC LIMIT ?"
				budgetSQL         = "SELECT CASE WHEN COUNT(*)<=? AND 2*COALESCE(SUM(n),0)<=? THEN 1 ELSE 0 END FROM (SELECT OCTET_LENGTH(`primary`)+COALESCE(OCTET_LENGTH(`select`),0) AS n FROM `schema`.`table` LIMIT ?) AS bounded"
				pageSQL           = "SELECT `primary`, `select` FROM `schema`.`table` FORCE INDEX(PRIMARY) ORDER BY `primary` ASC LIMIT ?"
				deleteSQL         = "DELETE FROM `schema`.`table` WHERE `primary` IN (?)"
				deleteMatchingSQL = "DELETE FROM `schema`.`table` WHERE `primary` IN (?)"
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
			i.assertStatements(budgetSQL, pageSQL, pageSQL, selectedSQL)
			if err := store.DeleteMatching(t.Context(), query.Options.Filter, fixtureBudget()); err != nil {
				t.Fatal(err)
			}
			i.assertStatements(budgetSQL, pageSQL, pageSQL, deleteMatchingSQL)
			if err := store.DeleteIDs(t.Context(), []string{"one"}); err != nil {
				t.Fatal(err)
			}
			i.assertStatements(deleteSQL)
		})
	}
}
