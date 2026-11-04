package oracle

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	go_ora "github.com/sijms/go-ora/v3"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const fixtureSpace vectorstore.SpaceID = "native-test-space"

func fixtureVector(_ string) []float64 { return []float64{1, 0} }
func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, doc := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: doc, Values: fixtureVector(doc.Text)})
	}
	return b
}
func fixtureBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10000, MaxBytes: 32 << 20, Timeout: time.Minute}
}
func closedDatabase(t *testing.T) (*sql.DB, error) {
	t.Helper()
	connector := go_ora.NewConnector("oracle://unused:unused@invalid.invalid:1/unused")
	db := sql.OpenDB(connector)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	closedErr := db.PingContext(t.Context())
	if closedErr == nil {
		t.Fatal("closed database accepted a connection")
	}
	return db, closedErr
}

func offlineStore(t *testing.T, db *sql.DB) *Store {
	t.Helper()
	return &Store{db: db, fullTable: "documents", tableName: "documents", idColumn: "id", contentColumn: "content", metadataColumn: "facts", embeddingColumn: "embedding", space: fixtureSpace, distanceMetric: DistanceCosine}
}

func TestClosedDatabaseFailuresPreserveEffectsAndCause(t *testing.T) {
	db, closedErr := closedDatabase(t)
	s := offlineStore(t, db)
	q := &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{Filter: filter.EQ("tenant", "alpha")}}
	if r, err := s.Query(t.Context(), q); r != nil || !errors.Is(err, vectorstore.ErrUnsupportedFilter) {
		t.Fatalf("filtered native query: %v, %v", r, err)
	}
	if r, err := s.QueryByScan(t.Context(), q, fixtureBudget()); r != nil || !errors.Is(err, closedErr) {
		t.Fatalf("scan: %v, %v", r, err)
	}
	if err := s.DeleteMatching(t.Context(), q.Options.Filter, fixtureBudget()); !errors.Is(err, closedErr) {
		t.Fatalf("delete matching: %v", err)
	}
	if err := s.DeleteIDs(t.Context(), []string{"a", "A", "a "}); !errors.Is(err, closedErr) {
		t.Fatal(err)
	}
	if err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "x", Text: "text"}})); !errors.Is(err, closedErr) {
		t.Fatal(err)
	}
}
func TestInvalidLaterRecordOrSpaceDoesNotReachDatabase(t *testing.T) {
	s := offlineStore(t, nil)
	b := fixtureBatch([]*document.Document{{ID: "valid", Text: "text"}, {ID: strings.Repeat("a", documentIDBytes+1), Text: "last"}})
	if err := s.Upsert(t.Context(), b); !errors.Is(err, vectorstore.ErrInvalidDocument) {
		t.Fatal(err)
	}
	q := &vectorstore.Query{Space: "another-space", Values: []float64{1, 0}}
	if res, err := s.Query(t.Context(), q); res != nil || !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatalf("%v %v", res, err)
	}
}
func TestConstructionPreservesNativeErrors(t *testing.T) {
	db, closedErr := closedDatabase(t)
	for _, initialize := range []bool{false, true} {
		config := StoreConfig{DB: db, Space: fixtureSpace, Dimensions: 2, InitializeSchema: initialize}
		store, err := NewStore(t.Context(), config)
		if !errors.Is(err, closedErr) || store != nil {
			t.Fatalf("construct: %v, %v", store, err)
		}
	}
}
