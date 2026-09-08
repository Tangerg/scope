package typesense

import (
	"strings"
	"testing"

	"github.com/typesense/typesense-go/v3/typesense/api"
)

// vector_distance carries no units: what it means is fixed by the field's
// vec_dist. Scoring an inner-product distance through the cosine mapping yields
// plausible values in the right range that rank results wrongly, and nothing
// later in the call can notice, so a mismatch has to fail at wiring.
func TestCheckVectorDistanceRefusesAMetricItCannotScore(t *testing.T) {
	t.Parallel()

	store := &Store{collectionName: "documents"}
	for _, sample := range []struct {
		name  string
		field api.Field
		want  string
	}{
		{
			name:  "cosine",
			field: api.Field{Name: embeddingField, VecDist: new("cosine")},
		},
		{
			name:  "absent means the provider default",
			field: api.Field{Name: embeddingField},
		},
		{
			name:  "inner product",
			field: api.Field{Name: embeddingField, VecDist: new("ip")},
			want:  `uses vec_dist "ip"; this store scores "cosine" only`,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			schema := &api.CollectionResponse{
				Name:   "documents",
				Fields: []api.Field{{Name: idField, Type: "string"}, sample.field},
			}
			err := store.checkVectorDistance(schema)
			if sample.want == "" {
				if err != nil {
					t.Fatalf("checkVectorDistance() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("checkVectorDistance() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}

// A collection without the embedding field cannot serve a vector search.
func TestCheckVectorDistanceRequiresTheEmbeddingField(t *testing.T) {
	t.Parallel()

	store := &Store{collectionName: "documents"}
	schema := &api.CollectionResponse{
		Name:   "documents",
		Fields: []api.Field{{Name: idField, Type: "string"}},
	}
	if err := store.checkVectorDistance(schema); err == nil ||
		!strings.Contains(err.Error(), "has no "+embeddingField+" field") {
		t.Fatalf("checkVectorDistance() = %v, want a missing-field error", err)
	}
	if err := store.checkVectorDistance(nil); err == nil {
		t.Fatal("checkVectorDistance(nil) = nil, want an error")
	}
}
