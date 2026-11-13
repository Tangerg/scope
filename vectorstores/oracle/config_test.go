package oracle

import (
	"strings"
	"testing"
)

func TestConfigValidationOwnsConstructionRequirements(t *testing.T) {
	db, _ := closedDatabase(t)
	valid := StoreConfig{DB: db, Space: fixtureSpace}
	if err := valid.Validate(); err != nil {
		t.Fatalf("existing schema with inferred width is invalid: %v", err)
	}
	for _, test := range []struct {
		name, want string
		change     func(*StoreConfig)
	}{
		{"space", "space ID", func(c *StoreConfig) { c.Space = "" }},
		{"database", "DB is required", func(config *StoreConfig) { config.DB = nil }},
		{"negative_width", "create dimensions must not be negative", func(config *StoreConfig) { config.CreateDimensions = -1 }},
		{"metric", "unsupported DistanceMetric", func(config *StoreConfig) { config.DistanceMetric = "unsupported" }},
		{"schema", "SchemaName", func(config *StoreConfig) { config.SchemaName = "bad.schema" }},
		{"table", "TableName", func(config *StoreConfig) { config.TableName = "bad.table" }},
		{"id", "IDColumn", func(config *StoreConfig) { config.IDColumn = "bad.id" }},
		{"content", "ContentColumn", func(config *StoreConfig) { config.ContentColumn = "bad.content" }},
		{"metadata", "MetadataColumn", func(config *StoreConfig) { config.MetadataColumn = "bad.metadata" }},
		{"embedding", "EmbeddingColumn", func(config *StoreConfig) { config.EmbeddingColumn = "bad.embedding" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.change(&config)
			if err := config.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate = %v, want %s", err, test.want)
			}
			store, err := NewStore(t.Context(), config)
			if err == nil || !strings.Contains(err.Error(), test.want) || store != nil {
				t.Fatalf("construction ignored configuration error: %#v, %v", store, err)
			}
		})
	}
}

func TestMetricsRetainCanonicalNamesAndScoreDirection(t *testing.T) {
	for _, test := range []struct {
		metric DistanceMetric
		name   string
	}{
		{DistanceCosine, "COSINE"},
		{DistanceEuclidean, "EUCLIDEAN"},
		{DistanceDot, "DOT"},
	} {
		if !test.metric.Valid() || test.metric.String() != test.name {
			t.Fatalf("metric %q: valid %v, name %q", test.metric, test.metric.Valid(), test.metric.String())
		}
		if near, far := test.metric.score(0), test.metric.score(1); near <= far {
			t.Fatalf("metric %s reversed distance: near %v, far %v", test.name, near, far)
		}
	}
	if DistanceMetric("unknown").Valid() {
		t.Fatal("unknown metric was accepted")
	}
}

func TestDDLKeepsExactIdentityAndLosslessMetadata(t *testing.T) {
	store := &Store{schemaName: "APP", tableName: "DOCUMENTS", idColumn: "ID", contentColumn: "CONTENT", metadataColumn: "FACTS", embeddingColumn: "EMBEDDING"}
	ddl := store.createTableStatement(3)
	for _, want := range []string{`CREATE TABLE IF NOT EXISTS "APP"."DOCUMENTS"`, `"ID" RAW(2000) NOT NULL PRIMARY KEY`, `"FACTS" BLOB NOT NULL`, `"EMBEDDING" VECTOR(3, FLOAT32) NOT NULL`} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("DDL = %s, want %s", ddl, want)
		}
	}
}
