package opensearch

import (
	"encoding/json"
	"errors"
	"testing"
)

// An existing index used to be accepted on existence alone. OpenSearch derives
// _score from the vector field's own space, and innerproduct is the only space
// whose score runs above 1, which is why SpaceType.score inverts that encoding
// and passes the others through. A store configured for one space against a
// field built for another either applies the inverse to a number it does not
// describe, or clamps unbounded inner-product scores onto Core's ceiling.
func TestValidateVectorField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		field      string
		spaceType  SpaceType
		dimensions int
		wantErr    bool
	}{
		{
			name:      "method carries the space type",
			field:     `{"type":"knn_vector","dimension":768,"method":{"name":"hnsw","engine":"lucene","space_type":"cosinesimil"}}`,
			spaceType: SpaceTypeCosine,
		},
		{
			// "This value can also be specified at the top level of the
			// mapping", and newer indices are written that way.
			name:      "field carries the space type",
			field:     `{"type":"knn_vector","dimension":768,"space_type":"innerproduct","method":{"name":"hnsw","engine":"faiss"}}`,
			spaceType: SpaceTypeIP,
		},
		{
			// It "defaults to l2" when neither the field nor the method
			// carries it, so an omitted space type is resolved, not ignored.
			name:      "omitted space type means l2",
			field:     `{"type":"knn_vector","dimension":768}`,
			spaceType: SpaceTypeL2,
		},
		{
			name:      "omitted space type disagrees with a cosine store",
			field:     `{"type":"knn_vector","dimension":768}`,
			spaceType: SpaceTypeCosine,
			wantErr:   true,
		},
		{
			name:      "space type disagrees",
			field:     `{"type":"knn_vector","dimension":768,"method":{"space_type":"l2"}}`,
			spaceType: SpaceTypeCosine,
			wantErr:   true,
		},
		{
			// Reading an inner-product score as any other space clamps every
			// value above 1 onto Core's ceiling.
			name:      "unbounded space read as bounded",
			field:     `{"type":"knn_vector","dimension":768,"space_type":"innerproduct"}`,
			spaceType: SpaceTypeL2,
			wantErr:   true,
		},
		{
			name:      "field and method disagree with each other",
			field:     `{"type":"knn_vector","dimension":768,"space_type":"l2","method":{"space_type":"cosinesimil"}}`,
			spaceType: SpaceTypeL2,
			wantErr:   true,
		},
		{
			// A trained field's space belongs to the model, not the mapping,
			// so defaulting it to l2 would be a guess.
			name:      "trained field states no space type",
			field:     `{"type":"knn_vector","dimension":768,"model_id":"model-1"}`,
			spaceType: SpaceTypeL2,
			wantErr:   true,
		},
		{
			name:      "field is absent",
			field:     `{}`,
			spaceType: SpaceTypeCosine,
			wantErr:   true,
		},
		{
			name:      "field is not a vector",
			field:     `{"type":"text"}`,
			spaceType: SpaceTypeCosine,
			wantErr:   true,
		},
		{
			name:       "width disagrees",
			field:      `{"type":"knn_vector","dimension":1536,"space_type":"cosinesimil"}`,
			spaceType:  SpaceTypeCosine,
			dimensions: 768,
			wantErr:    true,
		},
		{
			name:       "width agrees",
			field:      `{"type":"knn_vector","dimension":768,"space_type":"cosinesimil"}`,
			spaceType:  SpaceTypeCosine,
			dimensions: 768,
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
				spaceType:      test.spaceType,
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

// The space vocabulary is OpenSearch's own, spelled as the mapping spells it,
// so a normalized guess would never match what the index reports.
func TestSpaceTypeUsesOpenSearchSpelling(t *testing.T) {
	t.Parallel()

	for _, spaceType := range []SpaceType{"cosinesimil", "l2", "innerproduct", "l1", "linf"} {
		if !spaceType.Valid() {
			t.Errorf("SpaceType %q is not accepted by Valid()", spaceType)
		}
	}
	if SpaceTypeCosine != "cosinesimil" {
		t.Fatalf("SpaceTypeCosine = %q, want %q", SpaceTypeCosine, "cosinesimil")
	}
}
