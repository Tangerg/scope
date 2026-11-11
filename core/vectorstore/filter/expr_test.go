package filter_test

import (
	"math"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestConstructorsBuildRichImmutableNodes(t *testing.T) {
	expr := filter.And(
		filter.EQ("category", "tech"),
		filter.In("year", []int{2024, 2025}),
	)
	if err := expr.Validate(); err != nil {
		t.Fatal(err)
	}
	if expr.Operator() != filter.OpAnd {
		t.Fatalf("operator = %q, want and", expr.Operator())
	}
	left, ok := expr.Left().(*filter.BinaryExpr)
	if !ok || left.Operator() != filter.OpEqual {
		t.Fatalf("left = %#v, want equality", expr.Left())
	}
	literal, err := left.Literal()
	if err != nil || literal.Kind() != filter.LiteralString || literal.Text() != "tech" {
		t.Fatalf("literal = %#v, %v", literal, err)
	}

	right, ok := expr.Right().(*filter.BinaryExpr)
	if !ok {
		t.Fatalf("right = %T, want binary", expr.Right())
	}
	list, err := right.List()
	if err != nil {
		t.Fatal(err)
	}
	literals := list.Literals()
	literals[0] = filter.NewLiteral(0)
	if first, _ := list.First(); first.Text() != "2024" {
		t.Fatal("Literals exposed mutable list ownership")
	}
}

func TestParseReturnsValidatedCanonicalTree(t *testing.T) {
	expr, err := filter.Parse(`not (not (year >= 2020))`)
	if err != nil {
		t.Fatal(err)
	}
	binary, ok := expr.(*filter.BinaryExpr)
	if !ok || binary.Operator() != filter.OpGreaterEqual {
		t.Fatalf("parsed = %T %#v, want optimized comparison", expr, expr)
	}
	if binary.Start().Line != 1 || binary.Start().Column == 0 {
		t.Fatalf("parsed position = %v, want source position", binary.Start())
	}
	if binary.String() != "year >= 2020" {
		t.Fatalf("String() = %q", binary.String())
	}
}

func TestParseRejectsSemanticError(t *testing.T) {
	if _, err := filter.Parse(`name like 42`); err == nil {
		t.Fatal("LIKE with a numeric pattern must fail during Parse")
	}
}

func TestExpressionEqualityIgnoresSourcePosition(t *testing.T) {
	parsed, err := filter.Parse(`category == 'tech'`)
	if err != nil {
		t.Fatal(err)
	}
	built := filter.EQ("category", "tech")
	if !parsed.Equal(built) || !built.Equal(parsed) {
		t.Fatal("equivalent parsed and constructed expressions must be equal")
	}
}

func TestProgrammaticCompositePositionsRemainZero(t *testing.T) {
	parsed, err := filter.Parse(`category == 'tech'`)
	if err != nil {
		t.Fatal(err)
	}
	programmatic := filter.Not(parsed)
	if programmatic.Start() != (filter.Position{}) || programmatic.End() != (filter.Position{}) {
		t.Fatalf("programmatic position = %s..%s, want zero positions", programmatic.Start(), programmatic.End())
	}
}

func TestValidateRejectsInvalidConstructedValues(t *testing.T) {
	tests := map[string]filter.Predicate{
		"numeric identifier":   filter.EQ(filter.NewIdent("123"), 1),
		"keyword identifier":   filter.EQ(filter.NewIdent("and"), 1),
		"fractional index":     filter.EQ(filter.Index("field", 1.5), "value"),
		"negative index":       filter.EQ(filter.Index("field", -1), "value"),
		"oversized index":      filter.EQ(filter.Index("field", uint64(math.MaxUint64)), "value"),
		"non-finite number":    filter.EQ("score", math.NaN()),
		"numeric LIKE pattern": filter.Like("name", filter.NewLiteral(42)),
	}

	for name, expr := range tests {
		t.Run(name, func(t *testing.T) {
			if err := expr.Validate(); err == nil {
				t.Fatal("Validate returned nil error")
			}
		})
	}
}

func TestValidateNumericIndexBoundaries(t *testing.T) {
	if err := filter.EQ(filter.Index("items", int64(math.MaxInt64)), "value").Validate(); err != nil {
		t.Fatalf("max int64 index: %v", err)
	}
	if err := filter.EQ(filter.Index("items", uint64(math.MaxInt64)+1), "value").Validate(); err == nil {
		t.Fatal("Validate accepted an index above max int64")
	}
}

func TestNumericLiteralPreservesIntegerPrecision(t *testing.T) {
	literal := filter.NewLiteral(uint64(math.MaxUint64))
	if literal.Text() != "18446744073709551615" {
		t.Fatalf("Text = %q, want exact uint64 text", literal.Text())
	}
}

func TestNewLiteralCanonicalizesNegativeZero(t *testing.T) {
	literal := filter.NewLiteral(math.Copysign(0, -1))
	if literal.Text() != "0" {
		t.Fatalf("Text = %q, want 0", literal.Text())
	}
}

func TestComparisonInverseIsImmutable(t *testing.T) {
	original := filter.GT("score", 10)
	inverse, err := original.Inverse()
	if err != nil {
		t.Fatal(err)
	}
	if original.Operator() != filter.OpGreater || inverse.Operator() != filter.OpOr {
		t.Fatalf("operators = %s, %s", original.Operator(), inverse.Operator())
	}
	if _, err := filter.And(filter.EQ("a", 1), filter.EQ("b", 2)).Inverse(); err == nil || !strings.Contains(err.Error(), "inverse") {
		t.Fatalf("logical inverse error = %v", err)
	}
}

func TestComparisonInverseIncludesNullDomain(t *testing.T) {
	for _, predicate := range []*filter.BinaryExpr{filter.LT("x", 5), filter.LE("x", 5), filter.GT("x", 5), filter.GE("x", 5)} {
		inverse, err := predicate.Inverse()
		if err != nil {
			t.Fatal(err)
		}
		for _, values := range []map[string]any{{}, {"x": nil}, {"x": 4}, {"x": 5}, {"x": 6}, {"x": "invalid"}} {
			got, gotErr := filter.Match(inverse, facts(t, values))
			want, wantErr := filter.Match(filter.Not(predicate), facts(t, values))
			if got != want || (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("%s on %v: got %v %v, want %v %v", predicate, values, got, gotErr, want, wantErr)
			}
		}
	}
}

func TestClonedPredicatePreservesPositionsWithoutSharingNodes(t *testing.T) {
	original, err := filter.Parse("NOT (tags[0] IN ('red', 'green') AND year > 2010)")
	if err != nil {
		t.Fatal(err)
	}
	clone := filter.Clone(original)
	if !clone.Equal(original) || clone.Start() != original.Start() || clone.End() != original.End() {
		t.Fatalf("clone does not preserve the source tree: %v", clone)
	}
	logic := clone.(*filter.UnaryExpr).Right().(*filter.BinaryExpr)
	membership := logic.Left().(*filter.BinaryExpr)
	selector := membership.Left().(*filter.IndexExpr)
	*selector.Index() = *filter.NewLiteral(1)
	*membership.Right().(*filter.ListLiteral).Literals()[0] = *filter.NewLiteral("blue")
	kept := original.(*filter.UnaryExpr).Right().(*filter.BinaryExpr).Left().(*filter.BinaryExpr)
	if !kept.Left().(*filter.IndexExpr).Index().Equal(filter.NewLiteral(0)) || !kept.Right().(*filter.ListLiteral).Literals()[0].Equal(filter.NewLiteral("red")) {
		t.Fatal("clone shares selector or list elements with original")
	}
}

func TestPredicateGraphRejectsCyclesAndRetainsSharedBranches(t *testing.T) {
	leaf := filter.EQ("tenant", "red")
	cycle := filter.Not(leaf)
	*cycle = *filter.Not(cycle)
	if err := filter.Clone(cycle).Validate(); err == nil {
		t.Fatal("cyclic predicate was admitted")
	}
	index := filter.Index("tags", 0)
	*index = *filter.Index(index, 1)
	if err := filter.Clone(filter.EQ(index, "red")).Validate(); err == nil {
		t.Fatal("cyclic selector was admitted")
	}
	var shared filter.Predicate = leaf
	for range 64 {
		shared = filter.And(shared, shared)
	}
	cloned := filter.Clone(shared).(*filter.BinaryExpr)
	if err := cloned.Validate(); err != nil || cloned.Left() != cloned.Right() || cloned.Left() == shared.(*filter.BinaryExpr).Left() {
		t.Fatalf("copy lost shared-branch identity or isolation: %v", err)
	}
}
