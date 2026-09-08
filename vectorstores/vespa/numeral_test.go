package vespa

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// The literal's exact digits must survive into the provider filter. Rendering
// used to go through a Go scalar, where integer-ness was decided with
// float64(int64(value)) == value — an out-of-range float-to-int conversion Go
// leaves implementation-defined. At 2^63 arm64 saturates to MaxInt64, whose
// float64 compares equal, so the filter carried 9223372036854775807 while
// amd64 carried the right digits; an integer past int64 had no branch at all.
// Rendering from the literal removes both, and 1000000.0 stays a decimal
// numeral rather than the canonical "1e+06" no provider grammar documents.
func TestVisitorRendersExactNumerals(t *testing.T) {
	tests := []struct {
		name string
		expr filter.Predicate
		want string
	}{
		{name: "int64 boundary", expr: filter.EQ("n", float64(1<<63)), want: `metadata.n = 9223372036854776000`},
		{name: "past int64", expr: filter.EQ("n", uint64(1<<64-1)), want: `metadata.n = 18446744073709551615`},
		{name: "integral float", expr: filter.EQ("n", 1000000.0), want: `metadata.n = 1000000`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			visitor := newVisitor("metadata")
			if err := test.expr.Accept(visitor); err != nil {
				t.Fatal(err)
			}
			if got := visitor.snapshot(); got != test.want {
				t.Fatalf("snapshot() = %q, want %q", got, test.want)
			}
		})
	}
}
