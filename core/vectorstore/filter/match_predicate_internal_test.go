package filter

import (
	"math"
	"testing"
)

func TestEvaluatorUsesCompleteMetadataPath(t *testing.T) {
	predicate, err := Parse(`profile['name'] == 'scope'`)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{
		"profile": map[string]any{"name": "scope"},
	}
	matched, err := Match(predicate, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("nested metadata path did not match")
	}
}

func TestEvaluatorComparesLargeIntegersExactly(t *testing.T) {
	predicate := EQ("sequence", uint64(math.MaxUint64))
	matched, err := Match(predicate, map[string]any{
		"sequence": uint64(math.MaxUint64 - 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("distinct uint64 values collapsed through float64")
	}
}

func TestEvaluatorTreatsMissingFieldsAsNonMatches(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "ordering", source: `rank > 10`},
		{name: "pattern", source: `name like 'scope%'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			predicate, err := Parse(tt.source)
			if err != nil {
				t.Fatal(err)
			}
			matched, err := Match(predicate, map[string]any{"other": true})
			if err != nil {
				t.Fatalf("missing field returned error: %v", err)
			}
			if matched {
				t.Fatal("missing field matched predicate")
			}
		})
	}
}

func TestEvaluatorCollectionMembership(t *testing.T) {
	metadata := map[string]any{
		"tags":       []any{"go", "ai"},
		"priorities": []int{1, 2, 3},
		"scalar":     "go",
	}
	for _, test := range []struct {
		name      string
		predicate Predicate
		want      bool
	}{
		{name: "present string", predicate: Has("tags", "go"), want: true},
		{name: "absent string", predicate: Has("tags", "rust")},
		{name: "typed numeric slice", predicate: Has("priorities", 2), want: true},
		{name: "missing field", predicate: Has("missing", "go")},
		{name: "scalar does not masquerade as collection", predicate: Has("scalar", "go")},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Match(test.predicate, metadata)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Match() = %t, want %t", got, test.want)
			}
		})
	}
}
