package azureaisearch

import (
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestQueryPreservesAdmittedCosineBoundary(t *testing.T) {
	for _, test := range []struct {
		name    string
		raw     float64
		wantErr bool
	}{
		{"float32 lower endpoint", float64(math.Nextafter32(1.0/3, 0)), false},
		{"outside lower tolerance", math.Nextafter(float64(math.Nextafter32(1.0/3, 0)), 0), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store()
			fixture.put(&document.Document{ID: "opposite", Text: "opposite"})
			fixture.mu.Lock()
			row := fixture.rows["opposite"]
			err := row.Set("@search.score", test.raw)
			fixture.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			response, err := store.Query(t.Context(), &vectorstore.Query{
				Space: testSpace, Values: []float64{1, 0},
				Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeSemantic, TopK: 1},
			})
			if test.wantErr {
				if err == nil || response != nil {
					t.Fatalf("invalid native score produced response=%v, error=%v", response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Document.ID != "opposite" || response.Results[0].Score != 0 {
				t.Fatalf("boundary result = %#v, want opposite at score zero", response.Results)
			}
		})
	}
}
