package opensearch

import (
	"errors"
	"testing"
)

func TestStoredSourcePreservesRequiredFields(t *testing.T) {
	for _, sample := range []struct {
		name      string
		source    storedSource
		wantError bool
	}{
		{"default", storedSource{}, false},
		{"disabled", storedSource{Enabled: new(false)}, true},
		{"pruned metadata", storedSource{Excludes: []string{"metadata.author"}}, true},
		{"unrelated exclusion", storedSource{Excludes: []string{"embedding"}}, false},
		{"wildcard exclusion", storedSource{Excludes: []string{"meta*"}}, true},
		{"complete inclusion", storedSource{Includes: []string{"metadata", "content"}}, false},
		{"partial inclusion", storedSource{Includes: []string{"metadata.author", "content"}}, true},
		{"reconstructed", storedSource{Mode: "synthetic"}, true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			err := sample.source.validate("metadata", "content")
			if errors.Is(err, ErrIncompatibleIndex) != sample.wantError {
				t.Fatalf("validate() = %v; want incompatible=%v", err, sample.wantError)
			}
		})
	}
}
