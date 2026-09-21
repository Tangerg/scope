package filter_test

import (
	"encoding/json"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestParseOptimizerBooleanIdentities(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect filter.Predicate
	}{
		{
			name:   "triple negation",
			input:  `not not not active == true`,
			expect: filter.Not(filter.EQ("active", true)),
		},
		{
			name:   "and idempotence",
			input:  `a == 1 and a == 1`,
			expect: filter.EQ("a", 1),
		},
		{
			name:   "or idempotence",
			input:  `a == 1 or a == 1`,
			expect: filter.EQ("a", 1),
		},
		{
			name:   "and absorption",
			input:  `a == 1 and (a == 1 or b == 2)`,
			expect: filter.EQ("a", 1),
		},
		{
			name:   "or absorption reversed",
			input:  `(b == 2 and a == 1) or a == 1`,
			expect: filter.EQ("a", 1),
		},
		{
			name:  "associative deduplication",
			input: `(a == 1 and b == 2) and a == 1`,
			expect: filter.And(
				filter.EQ("a", 1),
				filter.EQ("b", 2),
			),
		},
		{
			name:   "deep absorption",
			input:  `a == 1 and (b == 2 or (c == 3 or a == 1))`,
			expect: filter.EQ("a", 1),
		},
		{
			name:  "commutative clause absorption",
			input: `(a == 1 or b == 2) and (b == 2 or a == 1 or c == 3)`,
			expect: filter.Or(
				filter.EQ("a", 1),
				filter.EQ("b", 2),
			),
		},
		{
			name:  "factor conjunction",
			input: `(a == 1 and b == 2) or (a == 1 and c == 3)`,
			expect: filter.And(
				filter.EQ("a", 1),
				filter.Or(filter.EQ("b", 2), filter.EQ("c", 3)),
			),
		},
		{
			name:  "factor disjunction",
			input: `(a == 1 or b == 2) and (a == 1 or c == 3)`,
			expect: filter.Or(
				filter.EQ("a", 1),
				filter.And(filter.EQ("b", 2), filter.EQ("c", 3)),
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := filter.Parse(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range []int{0, 1} {
				for _, b := range []int{0, 2} {
					for _, c := range []int{0, 3} {
						values := map[string]any{"a": a, "b": b, "c": c, "active": true}
						got, err := filter.Match(actual, values)
						want, wantErr := filter.Match(tt.expect, values)
						if err != nil || wantErr != nil || got != want {
							t.Fatalf("%s: got %v, %v; want %v, %v", tt.input, got, err, want, wantErr)
						}
					}
				}
			}
		})
	}
}

func TestValidateDoesNotRewriteProgrammaticPredicate(t *testing.T) {
	comparison := filter.EQ("a", 1)
	predicate := filter.And(comparison, comparison)

	if err := predicate.Validate(); err != nil {
		t.Fatal(err)
	}
	if predicate.Operator() != filter.OpAnd || predicate.Left() != comparison || predicate.Right() != comparison {
		t.Fatal("Validate rewrote the caller-owned predicate")
	}
}

func TestParseOptimizerPreservesMembershipOperands(t *testing.T) {
	predicate, err := filter.Parse(`status in ('active', 'active', 'paused')`)
	if err != nil {
		t.Fatal(err)
	}
	list := predicate.(*filter.BinaryExpr).Right().(*filter.ListLiteral)
	values := list.Literals()
	if len(values) != 3 || values[0].Text() != "active" || values[1].Text() != "active" {
		t.Fatalf("membership values = %#v, want source order and duplicates preserved", values)
	}
}

func TestFilterRoundTripPreservesEvaluationErrors(t *testing.T) {
	for _, predicate := range []filter.Predicate{
		filter.Or(filter.And(filter.GT("bad", 0), filter.EQ("a", 1)), filter.EQ("a", 1)),
		filter.And(filter.Or(filter.GT("bad", 0), filter.EQ("a", 1)), filter.EQ("a", 1)),
		filter.Or(filter.And(filter.GT("bad", 0), filter.EQ("a", 1)), filter.And(filter.EQ("a", 1), filter.EQ("b", 2))),
	} {
		encoded, err := json.Marshal(vectorstore.SearchOptions{Filter: predicate})
		if err != nil {
			t.Fatal(err)
		}
		var options vectorstore.SearchOptions
		if err := json.Unmarshal(encoded, &options); err != nil {
			t.Fatal(err)
		}
		for _, a := range []int{0, 1} {
			values := map[string]any{"a": a, "b": 2, "bad": "invalid"}
			before, beforeErr := filter.Match(predicate, values)
			after, afterErr := filter.Match(options.Filter, values)
			if before != after || beforeErr == nil || afterErr == nil || beforeErr.Error() != afterErr.Error() {
				t.Fatalf("roundtrip: %v %v -> %v %v", before, beforeErr, after, afterErr)
			}
		}
	}
}
