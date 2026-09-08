package oracle

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// The filter AST is two-valued: an absent metadata key evaluates as nil and
// filter.Match decides every comparison against it. SQL is three-valued, so a
// bare comparison on a missing key is UNKNOWN — which drops the row for any
// operator and stays UNKNOWN under NOT, dropping rows a negated filter should
// keep. Each leaf therefore carries the truth value the AST assigns an absent
// key.
func TestLeavesAreTotalOverAnAbsentKey(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		source string
		guard  string
	}{
		// filter.Match: false when the key is absent.
		{source: `author == 'Alice'`, guard: "json_value(metadata, '$.author') IS NOT NULL AND"},
		{source: `year > 2020`, guard: "json_value(metadata, '$.year') IS NOT NULL AND"},
		{source: `author like 'A%'`, guard: "json_value(metadata, '$.author') IS NOT NULL AND"},
		{source: `author in ('a','b')`, guard: "json_value(metadata, '$.author') IS NOT NULL AND"},
		// filter.Match: true when the key is absent, because nil is a value
		// and it is not equal to 'Alice'.
		{source: `author != 'Alice'`, guard: "json_value(metadata, '$.author') IS NULL OR"},
	} {
		t.Run(sample.source, func(t *testing.T) {
			sql := compileForAbsentKeyTest(t, sample.source)
			if !strings.Contains(sql, sample.guard) {
				t.Fatalf("sql = %q, want a leaf guarded by %q", sql, sample.guard)
			}
		})
	}
}

// A negated leaf has to invert a decided value, not an UNKNOWN, so the guard
// stays inside the NOT.
func TestNegationSeesADecidedLeaf(t *testing.T) {
	t.Parallel()

	sql := compileForAbsentKeyTest(t, `not (author == 'Alice')`)
	guardAt := strings.Index(sql, "IS NOT NULL AND")
	notAt := strings.Index(sql, "NOT (")
	if guardAt < 0 || notAt < 0 || guardAt < notAt {
		t.Fatalf("sql = %q, want the absent-key guard nested inside NOT", sql)
	}
}

// IS NULL was already total and must stay a bare test.
func TestNullTestNeedsNoGuard(t *testing.T) {
	t.Parallel()

	sql := compileForAbsentKeyTest(t, `author is null`)
	if got := strings.Count(sql, "IS NULL"); got != 1 {
		t.Fatalf("sql = %q, want exactly one IS NULL test", sql)
	}
}

func compileForAbsentKeyTest(t *testing.T, source string) string {
	t.Helper()
	expression, err := filter.Parse(source)
	if err != nil {
		t.Fatalf("parse %q: %v", source, err)
	}
	visitor := newVisitor("metadata")
	if acceptErr := expression.Accept(visitor); acceptErr != nil {
		t.Fatalf("compile %q: %v", source, acceptErr)
	}
	sql, _ := visitor.snapshot()
	return sql
}
