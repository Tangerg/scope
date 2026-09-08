package pgfilter_test

import (
	"strings"
	"testing"
)

// The filter AST is two-valued: an absent metadata key evaluates as nil and
// filter.Match decides every comparison against it. SQL is three-valued, so a
// bare comparison on a missing key is UNKNOWN — which drops the row for any
// operator and stays UNKNOWN under NOT, dropping rows a negated filter should
// keep. Each leaf therefore carries the truth value the AST assigns an absent
// key, which the emitted guard makes explicit.
func TestLeavesAreTotalOverAnAbsentKey(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		source string
		guard  string
	}{
		// filter.Match: false when the key is absent.
		{source: `author == 'Alice'`, guard: "metadata->>'author' IS NOT NULL AND"},
		{source: `year > 2020`, guard: "metadata->>'year' IS NOT NULL AND"},
		{source: `year <= 2020`, guard: "metadata->>'year' IS NOT NULL AND"},
		{source: `author like 'A%'`, guard: "metadata->>'author' IS NOT NULL AND"},
		{source: `author in ('a','b')`, guard: "metadata->>'author' IS NOT NULL AND"},
		// filter.Match: true when the key is absent, because nil is a value
		// and it is not equal to 'Alice'.
		{source: `author != 'Alice'`, guard: "metadata->>'author' IS NULL OR"},
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

// The guard reads the raw text extraction, not the cast one, so testing for a
// missing key never depends on whether the cast would succeed.
func TestAbsentKeyGuardSkipsTheCast(t *testing.T) {
	t.Parallel()

	sql, _, err := build(t, `year > 2020`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "metadata->>'year' IS NOT NULL AND (metadata->>'year')::numeric >") {
		t.Fatalf("sql = %q, want an uncast guard in front of the cast comparison", sql)
	}
}

// IS NULL was already total and must stay a bare test rather than gain a guard.
func TestNullTestNeedsNoGuard(t *testing.T) {
	t.Parallel()

	sql, _, err := build(t, `author is null`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := strings.Count(sql, "IS NULL"); got != 1 {
		t.Fatalf("sql = %q, want exactly one IS NULL test", sql)
	}
}
