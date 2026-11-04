package clickhouse

import (
	"database/sql/driver"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type testValueConverter struct{}

func (t testValueConverter) ConvertValue(value any) (driver.Value, error) { return value, nil }

const testSpace vectorstore.SpaceID = "encoder-v1"

func testBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: testSpace}
	for _, doc := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: doc, Values: []float64{1, 0}})
	}
	return b
}
func testScanRequest(p filter.Predicate) vectorstore.ScanRequest {
	return vectorstore.ScanRequest{Filter: p, Budget: testScanBudget()}
}

func testScanBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10000, MaxBytes: 64 << 20, Timeout: time.Minute}
}
func mockStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(testValueConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if expectationErr := mock.ExpectationsWereMet(); expectationErr != nil {
			t.Error(expectationErr)
		}
		mock.ExpectClose()
		if closeErr := db.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return &Store{db: db, databaseName: "scope", tableName: "documents", fullTable: "scope.documents", idColumn: "id", contentColumn: "content", metadataColumn: "metadata", embeddingColumn: "embedding", space: testSpace, dimensions: 2, distanceMetric: DistanceCosine}, mock
}

func TestSearchRetainsCoreMetadataAndIgnoresAnUnmatchedNativeRow(t *testing.T) {
	store, mock := mockStore(t)
	mock.ExpectExec("BEGIN TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT id, content, metadata").WillReturnRows(sqlmock.NewRows([]string{"id", "content", "metadata"}).AddRow("match", "text", `{"items":[7],"n":1e400,"s":"\u0041lice"}`).AddRow("miss", "text", `{"items":[8]}`)).RowsWillBeClosed()
	mock.ExpectQuery("SELECT id, content, metadata").WillReturnRows(sqlmock.NewRows([]string{"id", "content", "metadata"})).RowsWillBeClosed()
	mock.ExpectExec("COMMIT").WillReturnResult(sqlmock.NewResult(0, 0))
	predicate, err := filter.Parse(`items[0] == 7`)
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Scan(t.Context(), testScanRequest(predicate))
	if err != nil {
		t.Fatal(err)
	}
	if len(response) != 1 || response[0].ID != "match" {
		t.Fatalf("results = %+v", response)
	}
	if string(response[0].Metadata["n"]) != "1e400" {
		t.Fatal("encoded numeric fact changed")
	}
}

func TestStoredFilterFailuresPrecedeEmbeddingAndReturnNoResponse(t *testing.T) {
	for _, raw := range []string{`{"n":"wrong"}`, `{"n": invalid}`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			store, mock := mockStore(t)
			mock.ExpectExec("BEGIN TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery("SELECT id, content, metadata").WillReturnRows(sqlmock.NewRows([]string{"id", "content", "metadata"}).AddRow("a", "text", `{"n":7}`).AddRow("z", "text", raw)).RowsWillBeClosed()
			mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
			response, err := store.Scan(t.Context(), testScanRequest(filter.GT("n", 1)))
			if err == nil || response != nil {
				t.Fatalf("search = %+v, %v", response, err)
			}
		})
	}
}

func TestSearchRejectsInvalidOutputBeforeThresholding(t *testing.T) {
	for _, sample := range []struct {
		name, id, text string
		distance       float64
	}{
		{"missing ID", "", "text", 2}, {"missing text", "id", "", 2}, {"invalid score", "id", "text", math.NaN()},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store, mock := mockStore(t)
			mock.ExpectQuery("SELECT id, content, metadata, toFloat64").WillReturnRows(sqlmock.NewRows([]string{"id", "content", "metadata", "distance"}).AddRow(sample.id, sample.text, "null", sample.distance)).RowsWillBeClosed()
			response, err := store.Query(t.Context(), &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{MinScore: 0.9}})
			if err == nil || response != nil {
				t.Fatalf("invalid result hidden: %+v, %v", response, err)
			}
		})
	}
}

func TestIndexUsesCoreEncodingAndOwnsBatchFailure(t *testing.T) {
	for _, sendFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "sent", true: "failed"}[sendFailure], func(t *testing.T) {
			store, mock := mockStore(t)
			m := metadata.Map{}
			mock.ExpectBegin()
			prepared := mock.ExpectPrepare("INSERT INTO scope.documents")
			prepared.ExpectExec().WithArgs("id", "text", "{}", []float32{1, 0}).WillReturnResult(sqlmock.NewResult(0, 0))
			failure := errors.New("native insert failed")
			if sendFailure {
				mock.ExpectCommit().WillReturnError(failure)
			} else {
				mock.ExpectCommit()
			}
			err := store.Upsert(t.Context(), testBatch([]*document.Document{{ID: "id", Text: "text", Metadata: m}}))
			if sendFailure && !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if !sendFailure && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConstructionRejectsInvalidConfigBeforeIO(t *testing.T) {
	for _, sample := range []struct {
		name   string
		change func(*StoreConfig)
	}{
		{"missing database", func(c *StoreConfig) { c.DB = nil }},
		{"missing space", func(c *StoreConfig) { c.Space = "" }},
		{"negative dimensions", func(c *StoreConfig) { c.Dimensions = -1 }},
		{"missing creation dimensions", func(c *StoreConfig) { c.InitializeSchema = true }},
		{"unsupported metric", func(c *StoreConfig) { c.DistanceMetric = "other" }},
		{"same column for different facts", func(c *StoreConfig) { c.ContentColumn = DefaultIDColumn }},
		{"invalid identifier", func(c *StoreConfig) { c.IDColumn = "id;drop" }},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store, _ := mockStore(t)
			config := StoreConfig{DB: store.db, Space: testSpace, DatabaseName: "scope"}
			sample.change(&config)
			if _, err := NewStore(t.Context(), config); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestConstructionBindsCurrentDatabaseOnce(t *testing.T) {
	store, mock := mockStore(t)
	mock.ExpectExec("BEGIN TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT currentDatabase").WillReturnRows(sqlmock.NewRows([]string{"database"}).AddRow("scope"))
	mock.ExpectQuery("SELECT engine_full").WithArgs("scope", testSpace.StorageKey()+"_"+DefaultTableName).WillReturnRows(sqlmock.NewRows([]string{"engine", "sorting", "partition", "ddl"}).AddRow("ReplacingMergeTree ORDER BY id", "id", "", currentDDL))
	expectColumns(mock, "String", "")
	bound, err := NewStore(t.Context(), StoreConfig{DB: store.db, Space: testSpace})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("FROM scope." + testSpace.StorageKey() + "_vector_store FINAL").WillReturnRows(sqlmock.NewRows([]string{"id", "content", "metadata", "distance"}).AddRow("id", "text", "null", 0.0))
	if _, err := bound.Query(t.Context(), &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}}); err != nil {
		t.Fatal(err)
	}
}
