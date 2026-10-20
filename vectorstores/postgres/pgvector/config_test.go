package pgvector

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/vectorstores/postgres"
)

// TestStoreConfigRejectsAnUnusableConfiguration keeps a store from being
// constructed against a half-configured backend, where the failure would only
// appear at the first query as an opaque driver error.
func TestStoreConfigRejectsAnUnusableConfiguration(t *testing.T) {
	cases := map[string]StoreConfig{
		"missing pool":        {},
		"negative dimensions": {Dimensions: -1},
		"unknown metric":      {DistanceMetric: postgres.DistanceMetric("manhattan")},
		"unknown index":       {IndexType: IndexType("btree")},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if err := config.Validate(); err == nil {
				t.Fatal("an unusable configuration validated")
			}
		})
	}
}

// TestIdentifiersMustBeSQLSafe is the injection trust boundary: a table or
// column name reaches the query text directly, so anything outside the
// identifier grammar has to be refused at construction.
func TestIdentifiersMustBeSQLSafe(t *testing.T) {
	hostile := []string{
		`users; DROP TABLE documents`,
		`"quoted"`,
		"with space",
		"with-dash",
		"",
	}
	for _, value := range hostile {
		t.Run(value, func(t *testing.T) {
			if err := identifier(value).validate("TableName"); err == nil {
				t.Fatalf("identifier %q was accepted", value)
			}
		})
	}
	for _, value := range []string{"documents", "public", "vector_store_1", "_private"} {
		if err := identifier(value).validate("TableName"); err != nil {
			t.Errorf("identifier %q was rejected: %v", value, err)
		}
	}
}

// TestDefaultsFillEveryIdentifier keeps a zero config from producing an empty
// table or index name that would reach the query text.
func TestDefaultsFillEveryIdentifier(t *testing.T) {
	var config StoreConfig
	config.applyDefaults()
	for name, value := range map[string]string{
		"SchemaName":     config.SchemaName,
		"TableName":      config.TableName,
		"IndexName":      config.IndexName,
		"MetadataColumn": config.MetadataColumn,
	} {
		if strings.TrimSpace(value) == "" {
			t.Errorf("%s defaulted to empty", name)
		}
	}
	if !config.DistanceMetric.Valid() || !config.IndexType.Valid() {
		t.Fatalf("defaults produced an invalid metric or index: %q, %q", config.DistanceMetric, config.IndexType)
	}
}
