package clickhouse

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Metadata is stored as JSON text, so a string literal is compared against its
// quoted encoding and a bool against its JSON keyword. Binding the bare text
// compared `Alice` against the stored `"Alice"` and selected nothing.
func TestVisitorBindsStoredJSONText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want []any
	}{
		{name: "string", src: `author == 'Alice'`, want: []any{`"Alice"`}},
		{name: "bool", src: `published == true`, want: []any{"true"}},
		{name: "number", src: `year == 2020`, want: []any{int64(2020)}},
		{name: "in strings", src: `tag in ('a', 'b')`, want: []any{`"a"`, `"b"`}},
		// OpLike matches the whole value, so the pattern is quoted the same way
		// the stored value is.
		{name: "like", src: `title like '%foo%'`, want: []any{`"%foo%"`}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, args, err := build(t, test.src)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if len(args) != len(test.want) {
				t.Fatalf("args = %#v, want %#v", args, test.want)
			}
			for index, want := range test.want {
				if args[index] != want {
					t.Fatalf("args[%d] = %#v, want %#v", index, args[index], want)
				}
			}
		})
	}
}

// The AST reads an absent key and a null value as the same nil, so every leaf
// answers for both and negation composes.
func TestVisitorLeafAnswersForNil(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "equality requires a present non-null key",
			src:  `author == 'Alice'`,
			want: `(mapContains(metadata, 'author') AND metadata['author'] != 'null' AND metadata['author'] = ?)`,
		},
		{
			name: "inequality matches nil",
			src:  `author != 'Alice'`,
			want: `(NOT mapContains(metadata, 'author') OR metadata['author'] = 'null' OR metadata['author'] <> ?)`,
		},
		{
			name: "null test asks both questions",
			src:  `author is null`,
			want: `(NOT mapContains(metadata, 'author') OR metadata['author'] = 'null')`,
		},
		{
			name: "range converts exactly",
			src:  `year >= 2020`,
			want: `(mapContains(metadata, 'year') AND metadata['year'] != 'null' AND toDecimal128OrNull(metadata['year'], 18) >= ?)`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sql, _, err := build(t, test.src)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if sql != test.want {
				t.Fatalf("sql = %q, want %q", sql, test.want)
			}
		})
	}
}

// A non-numeric ordering comparison never reaches this visitor: Accept
// validates the predicate first, and the AST owns the rule that an ordering
// operand must be numeric. That is why the visitor has no branch for it —
// toFloat64OrZero used to be that branch, converting the stored text into a
// comparison against zero and deciding rows the filter never decided. This
// pins the ownership the removal depends on, so the branch cannot come back
// as the answer to a case the AST is already answering.
func TestNonNumericOrderingIsRefusedBeforeTheVisitor(t *testing.T) {
	t.Parallel()

	t.Run("parsed", func(t *testing.T) {
		for _, src := range []string{`author > 'Alice'`, `published >= false`} {
			_, err := filter.Parse(src)
			if err == nil {
				t.Fatalf("Parse(%q) = nil error, want a numeric-operand error", src)
			}
			if !strings.Contains(err.Error(), "right operand must be numeric") {
				t.Fatalf("Parse(%q) = %v, want the AST's numeric-operand error", src, err)
			}
		}
	})

	// GT's type parameter admits only a number or a pre-built literal, so a
	// bare string does not compile. Wrapping one in a literal is the only way
	// to express the shape at all, and Accept validates before it visits, so
	// even that reaches the rule instead of the visitor.
	t.Run("built", func(t *testing.T) {
		err := filter.GT("author", filter.NewLiteral("Alice")).Accept(newVisitor("metadata"))
		if err == nil {
			t.Fatal("Accept(GT with a string literal) = nil error, want a numeric-operand error")
		}
		if !strings.Contains(err.Error(), "right operand must be numeric") {
			t.Fatalf("Accept(GT with a string literal) = %v, want the AST's numeric-operand error", err)
		}
	})
}
