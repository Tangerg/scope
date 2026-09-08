package mongodb

import (
	"strings"
	"testing"
)

// Atlas rejects a numCandidates below limit, so the configured value can only
// raise the candidate pool, never lower it below what the request needs.
func TestSearchCandidatesNeverFallBelowResultLimit(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name       string
		configured int
		limit      int
		want       int
	}{
		{name: "configured floor applies", configured: DefaultNumCandidates, limit: 5, want: DefaultNumCandidates},
		{name: "limit raises the floor", configured: DefaultNumCandidates, limit: 500, want: 500},
		{name: "equal", configured: 32, limit: 32, want: 32},
		{name: "ceiling holds", configured: MaxNumCandidates, limit: MaxNumCandidates, want: MaxNumCandidates},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store := &Store{numCandidates: sample.configured}
			got, err := store.searchCandidates(sample.limit)
			if err != nil {
				t.Fatalf("searchCandidates(%d) = %v, want nil", sample.limit, err)
			}
			if got != sample.want {
				t.Fatalf("searchCandidates(%d) = %d, want %d", sample.limit, got, sample.want)
			}
			if got < sample.limit {
				t.Fatalf("searchCandidates(%d) = %d, which Atlas rejects", sample.limit, got)
			}
		})
	}
}

// A TopK past the ceiling cannot be served at all, so it fails locally instead
// of as an opaque provider validation error.
func TestSearchCandidatesRejectsUnservableResultLimit(t *testing.T) {
	t.Parallel()

	store := &Store{numCandidates: DefaultNumCandidates}
	_, err := store.searchCandidates(MaxNumCandidates + 1)
	if err == nil || !strings.Contains(err.Error(), "exceeds the 10000 candidates") {
		t.Fatalf("searchCandidates() = %v, want a ceiling error", err)
	}
}

// The configured floor is itself bounded by what Atlas accepts.
func TestConfigRejectsNumCandidatesAboveCeiling(t *testing.T) {
	t.Parallel()

	config := StoreConfig{
		Collection:      &countingCollection{},
		EmbeddingModel:  constantModel{},
		DocumentBatcher: upsertBatcher{},
		NumCandidates:   MaxNumCandidates + 1,
	}
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), "NumCandidates must be <= 10000") {
		t.Fatalf("Validate() = %v, want a NumCandidates ceiling error", err)
	}
}
