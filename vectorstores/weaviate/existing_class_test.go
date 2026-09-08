package weaviate

import (
	"errors"
	"strings"
	"testing"

	"github.com/weaviate/weaviate/entities/models"
)

// Existence is not agreement. Search converts Weaviate's distance into a Score
// with the metric from this store's own config, so a class that ranks by a
// different distance returns scores that are wrong rather than missing —
// nothing fails, the ranking is silently mis-scaled. initialize used to return
// as soon as the class existed, which accepted exactly that.
func TestCompareClassDistance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured DistanceMetric
		config     any
		wantErr    bool
	}{
		{
			name:       "agrees",
			configured: DistanceCosine,
			config:     map[string]any{"distance": "cosine"},
		},
		{
			name:       "disagrees",
			configured: DistanceCosine,
			config:     map[string]any{"distance": "l2-squared"},
			wantErr:    true,
		},
		{
			// Weaviate omits the key when the class uses its default, so the
			// omission has to read as cosine rather than as a refusal.
			name:       "omitted key is the Weaviate default",
			configured: DistanceCosine,
			config:     map[string]any{},
		},
		{
			name:       "omitted key still disagrees with a non-default",
			configured: DistanceL2Squared,
			config:     map[string]any{},
			wantErr:    true,
		},
		{
			name:       "config is not an object",
			configured: DistanceCosine,
			config:     "cosine",
			wantErr:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &Store{className: "Document", distanceMetric: test.configured}
			err := store.compareClassDistance(&models.Class{
				Class:             "Document",
				VectorIndexConfig: test.config,
			})
			if test.wantErr {
				if err == nil {
					t.Fatal("compareClassDistance() = nil error, want an incompatibility error")
				}
				if !errors.Is(err, ErrIncompatibleClass) {
					t.Fatalf("compareClassDistance() = %v, want %v", err, ErrIncompatibleClass)
				}
				if !strings.Contains(err.Error(), "Document") {
					t.Fatalf("compareClassDistance() = %v, want the class named", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("compareClassDistance() = %v, want nil", err)
			}
		})
	}
}
