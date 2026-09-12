package azureaisearch

import (
	"encoding/json"
	"errors"
	"testing"
)

// @search.score is metric-specific, so the store applies a metric-specific
// transformation to it. The configured metric used to be an unchecked
// obligation on the caller: getting it wrong returned plausible scores that
// were wrong, with MinScore filtering by a threshold in the wrong scale.
//
// The metric is three hops from the field -- field names a profile, the profile
// names an algorithm, and only the algorithm carries the metric -- so every hop
// is a place the index can fail to agree.
func TestValidateIndexMetric(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  string
		want    SimilarityMetric
		wantErr bool
	}{
		{
			name: "agrees",
			schema: `{
				"fields": [{"name": "vector", "vectorSearchProfile": "p"}],
				"vectorSearch": {
					"profiles": [{"name": "p", "algorithm": "a"}],
					"algorithms": [{"name": "a", "kind": "hnsw", "hnswParameters": {"metric": "cosine"}}]
				}
			}`,
			want: SimilarityCosine,
		},
		{
			name: "exhaustive knn carries the metric in its own block",
			schema: `{
				"fields": [{"name": "vector", "vectorSearchProfile": "p"}],
				"vectorSearch": {
					"profiles": [{"name": "p", "algorithm": "a"}],
					"algorithms": [{"name": "a", "kind": "exhaustiveKnn", "exhaustiveKnnParameters": {"metric": "dotProduct"}}]
				}
			}`,
			want: SimilarityDot,
		},
		{
			name: "metric disagrees",
			schema: `{
				"fields": [{"name": "vector", "vectorSearchProfile": "p"}],
				"vectorSearch": {
					"profiles": [{"name": "p", "algorithm": "a"}],
					"algorithms": [{"name": "a", "kind": "hnsw", "hnswParameters": {"metric": "euclidean"}}]
				}
			}`,
			want:    SimilarityCosine,
			wantErr: true,
		},
		{
			name:    "field is absent",
			schema:  `{"fields": [{"name": "other"}], "vectorSearch": {}}`,
			want:    SimilarityCosine,
			wantErr: true,
		},
		{
			// A field with no profile is stored but not vector-searchable, so
			// every query would come back empty for a reason the index states.
			name:    "field names no profile",
			schema:  `{"fields": [{"name": "vector"}], "vectorSearch": {}}`,
			want:    SimilarityCosine,
			wantErr: true,
		},
		{
			name: "profile names an absent algorithm",
			schema: `{
				"fields": [{"name": "vector", "vectorSearchProfile": "p"}],
				"vectorSearch": {"profiles": [{"name": "p", "algorithm": "missing"}], "algorithms": []}
			}`,
			want:    SimilarityCosine,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var schema indexSchema
			if err := json.Unmarshal([]byte(test.schema), &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
			err := (&schema).validateMetric("vector", test.want)
			if test.wantErr {
				if !errors.Is(err, ErrIncompatibleIndex) {
					t.Fatalf("validateMetric() = %v, want ErrIncompatibleIndex", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateMetric() = %v, want nil", err)
			}
		})
	}
}

// The ID field carries two jobs, and Azure locks in the attributes for both
// when the field is first added to the index: naming a document in a delete
// action needs the key, and paging through a filter's matches by key range --
// Azure's own "workaround for skip" -- needs that key filterable and sortable.
// Discovering either at delete time would be discovering it too late to fix.
func TestValidateIndexIDField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  string
		wantErr bool
	}{
		{
			name:   "key is filterable and sortable",
			schema: `{"fields": [{"name": "id", "key": true, "filterable": true, "sortable": true}]}`,
		},
		{
			name:    "field is absent",
			schema:  `{"fields": [{"name": "other", "key": true}]}`,
			wantErr: true,
		},
		{
			// The delete action identifies a document by its key, so a
			// non-key ID field names nothing to delete.
			name:    "field is not the key",
			schema:  `{"fields": [{"name": "id", "filterable": true, "sortable": true}]}`,
			wantErr: true,
		},
		{
			name:    "key is not sortable",
			schema:  `{"fields": [{"name": "id", "key": true, "filterable": true}]}`,
			wantErr: true,
		},
		{
			name:    "key is not filterable",
			schema:  `{"fields": [{"name": "id", "key": true, "sortable": true}]}`,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var schema indexSchema
			if err := json.Unmarshal([]byte(test.schema), &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
			err := (&schema).validateIDField("id")
			if test.wantErr {
				if !errors.Is(err, ErrIncompatibleIndex) {
					t.Fatalf("validateIDField() = %v, want ErrIncompatibleIndex", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateIDField() = %v, want nil", err)
			}
		})
	}
}

// The metric vocabulary is Azure's own, spelled as the REST surface spells it:
// dotProduct is camelCase while the other two are lowercase, so a normalized
// guess would never match.
func TestSimilarityMetricUsesAzureSpelling(t *testing.T) {
	t.Parallel()

	for _, metric := range []SimilarityMetric{"cosine", "euclidean", "dotProduct"} {
		if !metric.Valid() {
			t.Errorf("SimilarityMetric %q is not accepted by Valid()", metric)
		}
	}
	if SimilarityDot != "dotProduct" {
		t.Fatalf("SimilarityDot = %q, want %q", SimilarityDot, "dotProduct")
	}
}
