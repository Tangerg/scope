package clickhouse

import (
	"strings"
	"testing"
)

// A Map(String, String) subscript answers an absent key with the empty string
// and the numeric conversion answers with NULL, neither of which is the truth
// value the filter AST assigns. mapContains asks the question directly, which
// also keeps a negated leaf from inverting a NULL.
func TestLeavesAreTotalOverAnAbsentKey(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		source string
		guard  string
	}{
		{source: `author == 'Alice'`, guard: "(mapContains(metadata, 'author') AND"},
		{source: `year > 2020`, guard: "(mapContains(metadata, 'year') AND"},
		{source: `author like 'A%'`, guard: "(mapContains(metadata, 'author') AND"},
		{source: `author in ('a','b')`, guard: "(mapContains(metadata, 'author') AND"},
		{source: `author != 'Alice'`, guard: "(NOT mapContains(metadata, 'author') OR"},
	} {
		t.Run(sample.source, func(t *testing.T) {
			sql, _, err := build(t, sample.source)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !strings.Contains(sql, sample.guard) {
				t.Fatalf("sql = %q, want a leaf guarded by %q", sql, sample.guard)
			}
		})
	}
}

// Float64's 53-bit mantissa cannot hold every int64, so a numeric comparison
// converts to an exact decimal instead. The AST compares as a rational
// precisely so an integer is never rounded to a float's precision.
func TestNumericComparisonIsExact(t *testing.T) {
	t.Parallel()

	sql, _, err := build(t, `id == 9007199254740993`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "toDecimal128OrNull(metadata['id'], 18)") {
		t.Fatalf("sql = %q, want an exact decimal conversion", sql)
	}
	if strings.Contains(sql, "toFloat64") {
		t.Fatalf("sql = %q converts through an approximate float", sql)
	}
}

// Predicate parsing must survive a filter whose value cannot be parsed as a
// number; the conversion yields NULL, which the guard leaves excluded rather
// than turning into a zero that could satisfy a range.
func TestNonNumericValueDoesNotBecomeZero(t *testing.T) {
	t.Parallel()

	sql, _, err := build(t, `score > -1`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if strings.Contains(sql, "OrZero") {
		t.Fatalf("sql = %q still substitutes zero for an unparsable value", sql)
	}
}
