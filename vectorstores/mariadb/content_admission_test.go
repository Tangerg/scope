package mariadb

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestContentAdmissionUsesNativeByteCapacity(t *testing.T) {
	for _, test := range []struct {
		name     string
		dataType string
		capacity int64
		text     string
		accepted bool
	}{
		{"TEXT exact bytes", "text", 65535, strings.Repeat("x", 65535), true},
		{"TEXT one byte over", "text", 65535, strings.Repeat("x", 65536), false},
		{"UTF-8 exact bytes", "text", 65535, strings.Repeat("界", 21845), true},
		{"UTF-8 one byte over", "text", 65535, strings.Repeat("界", 21845) + "x", false},
		{"host TINYTEXT", "tinytext", 255, strings.Repeat("x", 256), false},
		{"host MEDIUMTEXT", "mediumtext", 16777215, strings.Repeat("x", 65536), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			statement := &writeStmt{}
			db := sql.OpenDB(resultConnector{stmt: statement, binding: []driver.Value{fixtureSpace.StorageKey(), "vector(2)", test.dataType, "utf8mb4", test.capacity}})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			store := offlineStore(t, db)
			docs := []*document.Document{{ID: "first", Text: "valid"}, {ID: "last", Text: test.text}}
			admissionErr := store.CheckDocuments(t.Context(), fixtureSpace, docs)
			writeErr := store.Upsert(t.Context(), fixtureBatch(docs))
			if test.accepted {
				if admissionErr != nil || writeErr != nil || len(statement.ids) != len(docs) {
					t.Fatalf("admission=%v upsert=%v writes=%v", admissionErr, writeErr, statement.ids)
				}
			} else if !errors.Is(admissionErr, vectorstore.ErrInvalidDocument) || !errors.Is(writeErr, vectorstore.ErrInvalidDocument) || len(statement.ids) != 0 {
				t.Fatalf("admission=%v upsert=%v writes=%v", admissionErr, writeErr, statement.ids)
			}
		})
	}
}

func TestContentBindingRejectsUnboundedOrLossySchemas(t *testing.T) {
	for _, test := range []struct {
		name     string
		dataType string
		charset  string
		capacity driver.Value
	}{
		{"character bound is not byte bound", "varchar", "utf8mb4", int64(65535)},
		{"non-Unicode text", "text", "latin1", int64(65535)},
		{"missing capacity", "text", "utf8mb4", nil},
		{"zero capacity", "text", "utf8mb4", int64(0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			statement := &writeStmt{}
			db := sql.OpenDB(resultConnector{stmt: statement, binding: []driver.Value{fixtureSpace.StorageKey(), "vector(2)", test.dataType, test.charset, test.capacity}})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			store := offlineStore(t, db)
			docs := []*document.Document{{ID: "document", Text: "text"}}
			if err := store.CheckDocuments(t.Context(), fixtureSpace, docs); err == nil {
				t.Fatal("incompatible native content column was admitted")
			}
			if err := store.Upsert(t.Context(), fixtureBatch(docs)); err == nil || len(statement.ids) != 0 {
				t.Fatalf("upsert=%v writes=%v", err, statement.ids)
			}
		})
	}
}

func TestContentAdmissionRechecksNativeCapacity(t *testing.T) {
	binding := []driver.Value{fixtureSpace.StorageKey(), "vector(2)", "text", "utf8mb4", int64(65535)}
	statement := &writeStmt{}
	db := sql.OpenDB(resultConnector{stmt: statement, binding: binding})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := offlineStore(t, db)
	docs := []*document.Document{{ID: "document", Text: strings.Repeat("x", 256)}}
	if err := store.CheckDocuments(t.Context(), fixtureSpace, docs); err != nil {
		t.Fatal(err)
	}
	binding[2], binding[4] = "tinytext", int64(255)
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); !errors.Is(err, vectorstore.ErrInvalidDocument) || len(statement.ids) != 0 {
		t.Fatalf("upsert=%v writes=%v", err, statement.ids)
	}
}
