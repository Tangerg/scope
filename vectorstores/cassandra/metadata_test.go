package cassandra

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
)

func recordStore() *Store {
	return &Store{
		keyspaceName:    DefaultKeyspaceName,
		tableName:       DefaultTableName,
		fullTable:       DefaultKeyspaceName + "." + DefaultTableName,
		idColumn:        DefaultIDColumn,
		contentColumn:   DefaultContentColumn,
		embeddingColumn: DefaultEmbeddingColumn,
		metadataColumn:  DefaultMetadataColumn,
		metadataColumns: []MetadataColumn{{Name: "tenant", CQLType: "text"}},
		similarity:      SimilarityCosine,
		dimensions:      3,
	}
}

// A search reads a document's metadata back whole, including a key that has no
// declared column. CQL reaches a key only as a declared column, so the typed
// columns could never carry an undeclared key — insertOne dropped it with no
// error and searchResultFromScan had nothing to read.
func TestSearchResultMetadataRoundTripsExactly(t *testing.T) {
	t.Parallel()

	source := metadata.Map{}
	values := map[string]any{
		"tenant": "acme",
		"year":   int64(2020),
		"score":  0.5,
		"active": true,
		"note":   nil,
		// No declared column, so this is the key the old projection lost.
		"undeclared": "kept",
	}
	for key, value := range values {
		if err := source.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}

	store := recordStore()
	destinations := store.scanDestinations()
	*destinations[scanDocumentIDIndex].(*string) = "one"
	*destinations[scanContentIndex].(*string) = "body"
	*destinations[scanScoreIndex].(*float32) = 1
	*destinations[scanMetadataIndex].(*string) = string(encoded)

	match, err := store.searchResultFromScan(destinations, 0)
	if err != nil {
		t.Fatalf("searchResultFromScan() = %v, want nil", err)
	}
	if match == nil {
		t.Fatal("searchResultFromScan() = nil, want a match")
	}

	roundTripped, err := match.Document.Metadata.Values()
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range values {
		if roundTripped[key] != want {
			t.Fatalf("metadata[%q] = %#v (%T), want %#v (%T)",
				key, roundTripped[key], roundTripped[key], want, want)
		}
	}
}

// Text that is not JSON cannot have come from this store's write path, so the
// read path reports it instead of handing back a document with silently empty
// metadata.
func TestSearchResultRejectsNonJSONMetadata(t *testing.T) {
	t.Parallel()

	store := recordStore()
	destinations := store.scanDestinations()
	*destinations[scanDocumentIDIndex].(*string) = "one"
	*destinations[scanContentIndex].(*string) = "body"
	*destinations[scanScoreIndex].(*float32) = 1
	*destinations[scanMetadataIndex].(*string) = "not json"

	if _, err := store.searchResultFromScan(destinations, 0); err == nil {
		t.Fatal("searchResultFromScan(non-JSON metadata) = nil error, want a decode error")
	}
}

// The generated table carries the record column, and it carries no SAI index:
// it is what a document reads back from, not something a filter selects on.
func TestGeneratedSchemaCarriesTheRecordColumn(t *testing.T) {
	t.Parallel()

	statements := recordStore().schemaStatements("{'class': 'SimpleStrategy', 'replication_factor': 1}")
	create := ""
	for _, statement := range statements {
		if strings.HasPrefix(statement, "CREATE TABLE") {
			create = statement
		}
		if strings.Contains(statement, "CREATE CUSTOM INDEX") &&
			strings.Contains(statement, "("+DefaultMetadataColumn+")") {
			t.Fatalf("the record column carries an SAI index: %q", statement)
		}
	}
	if !strings.Contains(create, DefaultMetadataColumn+" text") {
		t.Fatalf("CREATE TABLE = %q, want a %q text column", create, DefaultMetadataColumn)
	}
}

// A search selects the record column, because that is where a result reads its
// metadata from. The typed columns are absent: nothing reads them back.
func TestSearchSelectsTheRecordColumn(t *testing.T) {
	t.Parallel()

	store := recordStore()
	columns := store.selectColumns("[1, 2, 3]")
	if !slices.Contains(columns, DefaultMetadataColumn) {
		t.Fatalf("selected columns = %v, want the record column among them", columns)
	}
	if slices.Contains(columns, "tenant") {
		t.Fatalf("selected columns = %v, want no typed metadata column", columns)
	}
}

// Every column named here lands on the same table, so a reused name would have
// two writers and would not even be valid CQL.
func TestConfigRefusesCollidingColumnNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*StoreConfig)
	}{
		{
			name:   "record collides with content",
			mutate: func(c *StoreConfig) { c.MetadataColumn = c.ContentColumn },
		},
		{
			name: "declared column collides with the record",
			mutate: func(c *StoreConfig) {
				c.MetadataColumns = []MetadataColumn{{Name: DefaultMetadataColumn, CQLType: "text"}}
			},
		},
		{
			name: "declared columns repeat",
			mutate: func(c *StoreConfig) {
				c.MetadataColumns = []MetadataColumn{
					{Name: "tenant", CQLType: "text"},
					{Name: "tenant", CQLType: "text"},
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := StoreConfig{}
			config.applyDefaults()
			test.mutate(&config)
			if err := config.validateIdentifiers(); err == nil {
				t.Fatal("validateIdentifiers() = nil error, want a collision error")
			}
		})
	}
}
