package azurecosmos

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestLikeCompilationPreservesPatternSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		bound   string
	}{
		{pattern: "alpha", bound: "alpha"},
		{pattern: "alpha%", bound: "alpha%"},
		{pattern: "%alpha", bound: "%alpha"},
		{pattern: "%alpha%", bound: "%alpha%"},
		{pattern: "", bound: ""},
		{pattern: "%", bound: "%"},
		{pattern: "%%", bound: "%%"},
		{pattern: "a_b", bound: "a_b"},
		{pattern: "a%b", bound: "a%b"},
		{pattern: "a[0-9]b", bound: "a![0-9]b"},
		{pattern: "[^x]", bound: "![^x]"},
		{pattern: "[[", bound: "![!["},
		{pattern: "]", bound: "]"},
		{pattern: "!", bound: "!!"},
		{pattern: "![%_", bound: "!!![%_"},
		{pattern: `\_%`, bound: `\_%`},
		{pattern: "世界_\n%", bound: "世界_\n%"},
		{pattern: "' OR true --", bound: "' OR true --"},
	}

	for _, test := range tests {
		t.Run(test.pattern, func(t *testing.T) {
			t.Parallel()
			compiler := newVisitor("c", "metadata")
			if err := filter.Like("name", test.pattern).Accept(compiler); err != nil {
				t.Fatal(err)
			}
			query, params := compiler.snapshot()
			if want := "((c.metadata.name LIKE @p1 ESCAPE '!') ?? false)"; query != want {
				t.Fatalf("query = %q, want %q", query, want)
			}
			if len(params) != 1 || params[0].Name != "@p1" || params[0].Value != test.bound {
				t.Fatalf("params = %#v, want bound pattern %q", params, test.bound)
			}
		})
	}
}

func TestCollectionMembershipUsesArrayContains(t *testing.T) {
	t.Parallel()

	visitor := newVisitor("c", "metadata")
	if err := filter.Has("visible_to", "user-42").Accept(visitor); err != nil {
		t.Fatal(err)
	}
	query, params := visitor.snapshot()
	if want := "((ARRAY_CONTAINS(c.metadata.visible_to, @p1)) ?? false)"; query != want {
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
