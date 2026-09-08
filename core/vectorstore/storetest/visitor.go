package storetest

import (
	"slices"
	"strings"
	"testing"
)

// BuildFn parses a filter expression source and feeds it through the
// vendor's visitor. It returns nil on success, an error on failure.
// Implementations are responsible for assembling the AST (typically
// via [filter.Parse]) and driving the vendor visitor.
type BuildFn func(source string) error

// Options tunes the conformance suite for vendors with genuine
// capability gaps.
type Options struct {
	// Unsupported lists cases the vendor cannot represent exactly. The suite
	// verifies that each one returns an error; capability gaps must never turn
	// into silent approximations or unexecuted tests.
	Unsupported []string
}

// VisitorConformance runs the standard expression-coverage suite
// against a vendor's visitor.
//
// The case lists below are the union of what every backend's filter
// language must accept (success cases) and the known-rejected shapes
// every backend must error on (failure cases). Adding a new shape
// here exercises it across ALL vendors that opt into the suite — the
// single best lever for "no more silent visitor regressions on the
// 27th provider".
func VisitorConformance(t *testing.T, build BuildFn, options ...Options) {
	t.Helper()

	var opt Options
	if len(options) > 0 {
		opt = options[0]
	}

	success := []struct {
		name string
		src  string
	}{
		{"equality_string", `author == 'Alice'`},
		{"equality_number", `year == 2020`},
		{"equality_bool", `published == true`},
		{"inequality", `author != 'Alice'`},
		{"lt", `n < 10`},
		{"lte", `n <= 10`},
		{"gt", `n > 10`},
		{"gte", `n >= 10`},
		{"and", `a == 1 and b == 2`},
		{"or", `a == 1 or b == 2`},
		{"not", `not (a == 1)`},
		{"in_single", `tags in ('a')`},
		{"in_strings", `tags in ('a', 'b', 'c')`},
		{"in_numbers", `years in (2020, 2021, 2022)`},
		{"in_bools", `flags in (true, false)`},
		{"collection_membership", `tags has 'a'`},
		{"like", `title like '%foo%'`},
		{"indexed_key", `profile['author'] == 'Alice'`},
		{"nested_index", `profile['a']['b'] == 'x'`},
		{"nested_logical", `(a == 1 and b == 2) or (c == 3 and not (d == 4))`},
		// IS is in the operator set, so a compiler owes it an answer.
		// Omitting these let twelve adapters ship without handling a null
		// test at all, which nothing else noticed.
		{"null_test", `author is null`},
		{"not_null_test", `author is not null`},
	}
	for _, tc := range success {
		t.Run("Success_"+tc.name, func(t *testing.T) {
			unsupported := slices.Contains(opt.Unsupported, tc.name)
			err := build(tc.src)
			if unsupported {
				if err == nil {
					t.Fatalf("expected explicit unsupported error on %q, got nil", tc.src)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected success on %q, got error: %v", tc.src, err)
			}
		})
	}

	failure := []struct {
		name string
		src  string
		// hint is an optional substring expected in the error message.
		// Empty hint means "any error is acceptable" — useful when
		// vendors wrap with their own prefixes and the test suite
		// should avoid over-coupling to wording.
		hint string
	}{
		// LIKE with a non-string right side hits every backend's pattern
		// validation.
		{"like_number", `title like 42`, ""},
	}
	for _, tc := range failure {
		t.Run("Failure_"+tc.name, func(t *testing.T) {
			err := build(tc.src)
			if err == nil {
				t.Fatalf("expected error on %q, got nil", tc.src)
			}
			if tc.hint != "" && !strings.Contains(err.Error(), tc.hint) {
				// Hint mismatch is informational only — vendors that
				// wrap errors with their own prefixes still pass the
				// suite as long as they error at all.
				t.Logf("err = %v (hint %q not in error — fine if vendor wraps)", err, tc.hint)
			}
		})
	}
}
