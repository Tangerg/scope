package elasticsearch

import (
	"encoding/json"
	"errors"
	"testing"
)

// An existing index used to be accepted on existence alone. Elasticsearch
// derives _score from the vector field's own metric, so a store configured for
// one metric against a field built for another returns plausible scores in the
// wrong scale, with MinScore filtering by a threshold that means something
// else. Neither similarity nor dims can be changed after the field is created,
// so construction is the only place the disagreement is worth raising.
func TestValidateVectorField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		field      string
		similarity SimilarityFunction
		dimensions int
		wantErr    bool
	}{
		{
			name:       "agrees",
			field:      `{"type":"dense_vector","dims":768,"similarity":"cosine","index":true}`,
			similarity: SimilarityCosine,
			dimensions: 768,
		},
		{
			// Elasticsearch omits what was left at its default, and similarity
			// "defaults to l2_norm when element_type: bit, otherwise it
			// defaults to cosine".
			name:       "omitted similarity means cosine",
			field:      `{"type":"dense_vector","dims":768}`,
			similarity: SimilarityCosine,
		},
		{
			name:       "omitted similarity on a bit vector means l2_norm",
			field:      `{"type":"dense_vector","dims":768,"element_type":"bit"}`,
			similarity: SimilarityL2,
		},
		{
			name:       "similarity disagrees",
			field:      `{"type":"dense_vector","dims":768,"similarity":"l2_norm"}`,
			similarity: SimilarityCosine,
			wantErr:    true,
		},
		{
			// max_inner_product scores are "max_inner_product(query, vector)
			// + 1", unbounded above, so Core's range would flatten every
			// strong match onto one value. This store cannot name the metric,
			// so the comparison is what rejects it.
			name:       "unbounded metric is refused",
			field:      `{"type":"dense_vector","dims":768,"similarity":"max_inner_product"}`,
			similarity: SimilarityCosine,
			wantErr:    true,
		},
		{
			// index "defaults to true", and with it off "you can only use
			// exact brute-force search" -- not the knn query Search sends.
			name:       "index is off",
			field:      `{"type":"dense_vector","dims":768,"similarity":"cosine","index":false}`,
			similarity: SimilarityCosine,
			wantErr:    true,
		},
		{
			name:       "field is absent",
			field:      `{}`,
			similarity: SimilarityCosine,
			wantErr:    true,
		},
		{
			name:       "field is not a vector",
			field:      `{"type":"text"}`,
			similarity: SimilarityCosine,
			wantErr:    true,
		},
		{
			name:       "width disagrees",
			field:      `{"type":"dense_vector","dims":1536,"similarity":"cosine"}`,
			similarity: SimilarityCosine,
			dimensions: 768,
			wantErr:    true,
		},
		{
			// A store that declares no width is attaching to whatever the
			// index already holds, so there is nothing to disagree with.
			name:       "no configured width accepts any",
			field:      `{"type":"dense_vector","dims":1536,"similarity":"cosine"}`,
			similarity: SimilarityCosine,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var field storedVectorField
			if err := json.Unmarshal([]byte(test.field), &field); err != nil {
				t.Fatalf("decode field: %v", err)
			}
			store := &Store{
				indexName:      "documents",
				embeddingField: "embedding",
				similarity:     test.similarity,
				dimensions:     test.dimensions,
			}
			err := store.validateVectorField(field)
			if test.wantErr {
				if !errors.Is(err, ErrIncompatibleIndex) {
					t.Fatalf("validateVectorField() = %v, want ErrIncompatibleIndex", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateVectorField() = %v, want nil", err)
			}
		})
	}
}

// The similarity vocabulary is Elasticsearch's own, spelled as the mapping
// spells it, so a normalized guess would never match what the index reports.
func TestSimilarityUsesElasticsearchSpelling(t *testing.T) {
	t.Parallel()

	for _, similarity := range []SimilarityFunction{"cosine", "l2_norm", "dot_product"} {
		if !similarity.Valid() {
			t.Errorf("SimilarityFunction %q is not accepted by Valid()", similarity)
		}
	}
	if SimilarityL2 != "l2_norm" {
		t.Fatalf("SimilarityL2 = %q, want %q", SimilarityL2, "l2_norm")
	}
}
