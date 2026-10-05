package mariadb

import (
	"strings"
	"testing"
)

func TestSearchUsesCanonicalRowsAndConfiguredMetric(t *testing.T) {
	for _, test := range []struct {
		metric DistanceMetric
		want   string
	}{
		{DistanceCosine, "vec_distance_cosine("},
		{DistanceEuclidean, "vec_distance_euclidean("},
	} {
		t.Run(string(test.metric), func(t *testing.T) {
			store := &Store{fullTable: "docs", tableName: "docs", idColumn: "id", contentColumn: "content", metadataColumn: "metadata", embeddingColumn: "embedding", dimensions: 3, distanceMetric: test.metric}
			ddl := store.createTableStatement()
			if strings.Contains(ddl, "VECTOR INDEX") || !strings.Contains(ddl, "id VARBINARY(3072) NOT NULL PRIMARY KEY") || !strings.Contains(ddl, "embedding VECTOR(3) NOT NULL") {
				t.Fatalf("schema adds a competing identity or search projection: %s", ddl)
			}
			query := store.searchStatement(" AND predicate")
			for _, want := range []string{test.want, "FROM docs FORCE INDEX(PRIMARY) WHERE 1=1 AND predicate", "ORDER BY distance ASC, id ASC LIMIT ?"} {
				if !strings.Contains(query, want) {
					t.Fatalf("query = %q, want %q", query, want)
				}
			}
		})
	}
}
