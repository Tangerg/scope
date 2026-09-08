package mariadb

import (
	"strings"
	"testing"
)

// MariaDB builds a vector index for exactly one distance function and defaults
// that to euclidean, then uses the index only when the ORDER BY names the same
// function — otherwise "the index is not used and a full table scan is
// performed instead". The DDL omitted DISTANCE while this store's default
// metric is cosine, so InitializeSchema built an index no search could use and
// every query degraded to brute force. The rows came back correct, which is why
// nothing caught it.
//
// The index and the query are two halves of one decision, so they are asserted
// together: whichever metric is configured has to appear on both sides.
func TestVectorIndexMatchesTheSearchDistanceFunction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		metric        DistanceMetric
		wantIndex     string
		wantQueryCall string
	}{
		{metric: DistanceCosine, wantIndex: "DISTANCE=cosine", wantQueryCall: "vec_distance_cosine("},
		{metric: DistanceEuclidean, wantIndex: "DISTANCE=euclidean", wantQueryCall: "vec_distance_euclidean("},
	}

	for _, test := range tests {
		t.Run(string(test.metric), func(t *testing.T) {
			t.Parallel()

			store := &Store{
				fullTable:       "docs",
				tableName:       "docs",
				idColumn:        "id",
				contentColumn:   "content",
				metadataColumn:  "metadata",
				embeddingColumn: "embedding",
				dimensions:      3,
				distanceMetric:  test.metric,
			}

			ddl := store.createTableStatement()
			if !strings.Contains(ddl, test.wantIndex) {
				t.Fatalf("createTableStatement() = %q, want it to contain %q", ddl, test.wantIndex)
			}
			query := store.searchStatement("")
			if !strings.Contains(query, test.wantQueryCall) {
				t.Fatalf("searchStatement() = %q, want it to contain %q", query, test.wantQueryCall)
			}
		})
	}
}

// The optimizer's other two conditions are part of the same bargain: the
// distance has to stay a bare aliased call that ORDER BY names, sorted
// ascending, with a LIMIT. Wrapping it in an expression, or filtering on it in
// the WHERE clause, turns the search back into a full scan.
func TestSearchStatementKeepsTheIndexUsable(t *testing.T) {
	t.Parallel()

	store := &Store{
		fullTable:       "docs",
		tableName:       "docs",
		idColumn:        "id",
		contentColumn:   "content",
		metadataColumn:  "metadata",
		embeddingColumn: "embedding",
		dimensions:      3,
		distanceMetric:  DistanceCosine,
	}

	query := store.searchStatement(" AND JSON_VALUE(metadata, '$.k') = 'v'")
	for _, want := range []string{
		"vec_distance_cosine(embedding, VEC_FromText(?)) AS distance",
		"ORDER BY distance ASC",
		"LIMIT ?",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("searchStatement() = %q, want it to contain %q", query, want)
		}
	}
	if strings.Contains(query, "WHERE 1=1 AND vec_distance") {
		t.Fatalf("searchStatement() = %q, want no distance predicate in the WHERE clause", query)
	}
}
