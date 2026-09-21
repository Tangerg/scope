package azurecosmos

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestLikeCompilationPreservesPatternSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		want    string
	}{
		{pattern: "alpha", want: "c.metadata.name = @p1"},
		{pattern: "alpha%", want: "STARTSWITH(c.metadata.name, @p1)"},
		{pattern: "%alpha", want: "ENDSWITH(c.metadata.name, @p1)"},
		{pattern: "%alpha%", want: "CONTAINS(c.metadata.name, @p1)"},
	}

	for _, test := range tests {
		t.Run(test.pattern, func(t *testing.T) {
			t.Parallel()
			compiler := newVisitor("c", "metadata")
			if err := filter.Like("name", test.pattern).Accept(compiler); err != nil {
				t.Fatal(err)
			}
			if got, _ := compiler.snapshot(); got != test.want {
				t.Fatalf("Result() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLikeCompilationRejectsUnsupportedPatterns(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{"a_b", "a%b", "%"} {
		compiler := newVisitor("c", "metadata")
		if err := filter.Like("name", pattern).Accept(compiler); err == nil {
			t.Errorf("Visit(LIKE %q) error = nil, want unsupported pattern", pattern)
		}
	}
}

func TestCollectionMembershipUsesArrayContains(t *testing.T) {
	t.Parallel()

	visitor := newVisitor("c", "metadata")
	if err := filter.Has("visible_to", "user-42").Accept(visitor); err != nil {
		t.Fatal(err)
	}
	query, params := visitor.snapshot()
	if want := "ARRAY_CONTAINS(c.metadata.visible_to, @p1)"; query != want {
		t.Fatalf("Result() = %q, want %q", query, want)
	}
	if len(params) != 1 || params[0].Name != "@p1" || params[0].Value != "user-42" {
		t.Fatalf("params = %#v", params)
	}
}

func TestFieldPathPreservesLiteralKeysAndIndexes(t *testing.T) {
	for _, sample := range []struct{ expression, want string }{
		{`profile['a.b'] == 'keep'`, `c.metadata.profile["a.b"]`},
		{`profile['a b'] == 'keep'`, `c.metadata.profile["a b"]`},
		{`profile['0'] == 'keep'`, `c.metadata.profile["0"]`},
		{`profile[0] == 'keep'`, `c.metadata.profile[0]`},
	} {
		predicate, err := filter.Parse(sample.expression)
		if err != nil {
			t.Fatal(err)
		}
		path, err := newVisitor("c", "metadata").fieldPath(predicate.(*filter.BinaryExpr))
		if err != nil || path != sample.want {
			t.Fatalf("path=%q err=%v, want %q", path, err, sample.want)
		}
	}
}
