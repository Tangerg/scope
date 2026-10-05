package vespa

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Core retains exact integer digits; native YQL limits integer tokens to int64.
func TestVisitorRendersExactNumerals(t *testing.T) {
	tests := []struct {
		name        string
		expr        filter.Predicate
		want        string
		unsupported bool
	}{
		{name: "int64 boundary", expr: filter.EQ("n", float64(1<<63)), unsupported: true},
		{name: "past int64", expr: filter.EQ("n", uint64(1<<64-1)), unsupported: true},
		{name: "int64 maximum", expr: filter.EQ("n", int64(1<<63-1)), want: `(scope_metadata_paths contains "[\"n\"]" and n = 9223372036854775807)`},
		{name: "int64 minimum", expr: filter.EQ("n", int64(-1<<63)), want: `(scope_metadata_paths contains "[\"n\"]" and n = -9223372036854775808)`},
		{name: "integral float", expr: filter.EQ("n", 1000000.0), want: `(scope_metadata_paths contains "[\"n\"]" and n = 1000000)`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			visitor := newVisitor(schemaFields{})
			err := test.expr.Accept(visitor)
			if test.unsupported {
				if err == nil || visitor.snapshot() != "" {
					t.Fatalf("unrepresentable integer accepted: %q, error = %v", visitor.snapshot(), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := visitor.snapshot(); got != test.want {
				t.Fatalf("snapshot() = %q, want %q", got, test.want)
			}
		})
	}
}
