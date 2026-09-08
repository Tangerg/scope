package storetest

import (
	"slices"
	"strconv"
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

	// InterpolatesKeyPaths declares that this compiler writes a metadata key
	// into the query language as text rather than binding it as a value.
	//
	// It decides which way the suite reads a key the target language cannot
	// name. An indexed key is a string literal, so the caller chooses its
	// bytes; a compiler that pastes them into query text has the caller's key
	// read as syntax — profile['a:1 OR b'] compiled to Lucene as
	// profile.a:1 OR b, and the same shape reached Typesense's filter_by,
	// Vespa's YQL, an OData filter and a RediSearch tag clause. None of those
	// languages can quote a field name, so such a key has to be refused, and
	// the suite requires it.
	//
	// A compiler that binds the key instead — a SQL map subscript, a BSON
	// field name, a JSON object key — is not exposed, and the suite requires
	// the opposite: it must keep accepting any key, because refusing one would
	// take away a document it can otherwise filter perfectly well.
	InterpolatesKeyPaths bool

	// CompileText compiles a filter and returns the query text it produced.
	//
	// Set it when the compiler's whole output is text, because then a number
	// has to be written as a numeral and the digits are the only thing standing
	// between the caller's filter and a different one. Six compilers derived
	// those digits from a Go scalar and decided integer-ness with
	// float64(int64(value)) == value — an out-of-range float-to-int conversion
	// Go leaves implementation-defined — so at 2^63 arm64 emitted
	// 9223372036854775807 while amd64 emitted the right digits. Every existing
	// test passed: nothing compared the digits to anything.
	//
	// Leave it nil when the compiler binds values as arguments or builds a
	// provider structure. There is no numeral to get wrong then, and rendering
	// one for the suite's benefit would assert something the store never sends.
	CompileText func(source string) (string, error)

	// NumericDomainIsFloat64 declares that the provider's numeric fields are
	// doubles, so a numeral only has to denote the same double.
	//
	// RediSearch is the case: its NUMERIC range bounds are doubles, which is
	// why that store refuses an integer past 2^53 outright. An integer that a
	// double does hold exactly — 2^63, being a power of two — then comes out as
	// the shortest decimal that reads back as the same double, which is
	// 9223372036854776000 rather than 9223372036854775808. Demanding the
	// literal's digits there would demand precision the field cannot keep, so
	// the suite asks only that the numeral read back as the same double, which
	// still catches a digit lost or invented along the way.
	NumericDomainIsFloat64 bool
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

	// A key the target language cannot name: refused by a compiler that writes
	// keys as text, accepted by one that binds them. Both directions are
	// asserted, so neither an injection nor a needless refusal can appear
	// without this suite noticing.
	unnameable := []struct {
		name string
		src  string
	}{
		{"query_syntax", `profile['a:1 OR b'] == 'x'`},
		{"spaces", `profile['a b'] == 'x'`},
		{"quote", `profile['a"b'] == 'x'`},
		{"brace", `profile['a}|@b'] == 'x'`},
	}
	for _, tc := range unnameable {
		t.Run("UnnameableKey_"+tc.name, func(t *testing.T) {
			err := build(tc.src)
			if opt.InterpolatesKeyPaths {
				if err == nil {
					t.Fatalf("expected %q to be refused: this compiler writes keys into query text", tc.src)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected %q to compile: this compiler binds keys as values, got %v", tc.src, err)
			}
		})
	}

	if opt.CompileText != nil {
		runNumeralCases(t, opt.CompileText, opt.NumericDomainIsFloat64)
	}
}

// runNumeralCases requires a compiler that emits text to emit the literal's
// exact digits.
//
// The assertion is provider-independent because the digits are: whatever syntax
// surrounds it, a numeral that reads as a different number is a different
// filter. Each case is a value a re-derived numeral gets wrong — a magnitude no
// float64 holds, the int64 boundary where Go's out-of-range conversion is
// implementation-defined, and an integral float whose canonical form is
// exponential while no provider grammar here documents exponents.
func runNumeralCases(t *testing.T, compile func(string) (string, error), float64Domain bool) {
	t.Helper()

	cases := []struct {
		name   string
		src    string
		digits string
	}{
		{name: "past_int64", src: `n == 18446744073709551615`, digits: "18446744073709551615"},
		{name: "int64_boundary", src: `n == 9223372036854775808`, digits: "9223372036854775808"},
		{name: "float_int64_boundary", src: `n == 9223372036854775808.0`, digits: "9223372036854776000"},
		{name: "integral_float", src: `n == 1000000.0`, digits: "1000000"},
		{name: "fraction", src: `n == 0.8`, digits: "0.8"},
	}
	for _, tc := range cases {
		t.Run("Numeral_"+tc.name, func(t *testing.T) {
			text, err := compile(tc.src)
			if err != nil {
				// A compiler may refuse a magnitude it cannot carry — redis
				// refuses an integer RediSearch cannot hold exactly — but it
				// must refuse rather than round.
				t.Skipf("compiler refused %q: %v", tc.src, err)
			}
			if strings.Contains(text, tc.digits) {
				return
			}
			if float64Domain {
				assertSameFloat64(t, tc.src, text, tc.digits)
				return
			}
			t.Fatalf("compiled %q to %q, want the digits %s", tc.src, text, tc.digits)
		})
	}
}

// assertSameFloat64 accepts any numeral in the emitted text that reads back as
// the same double as the expected digits. A provider whose numeric field is a
// double cannot tell the two apart, so requiring one spelling would require
// precision the field does not keep — but a numeral that reads as a different
// double is a different filter on any provider.
func assertSameFloat64(t *testing.T, source, text, digits string) {
	t.Helper()

	want, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		t.Fatalf("test case digits %q are not a number: %v", digits, err)
	}
	for _, candidate := range numeralsIn(text) {
		if got, err := strconv.ParseFloat(candidate, 64); err == nil && got == want {
			return
		}
	}
	t.Fatalf("compiled %q to %q, want a numeral reading back as %v", source, text, want)
}

// numeralsIn pulls the numeral-shaped runs out of query text. The surrounding
// syntax is the provider's, so the scan stays deliberately loose: it only has
// to find the candidates, and ParseFloat decides which of them is a number.
func numeralsIn(text string) []string {
	var numerals []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			numerals = append(numerals, current.String())
			current.Reset()
		}
	}
	for _, character := range text {
		switch {
		case character >= '0' && character <= '9',
			character == '.', character == '-', character == '+',
			character == 'e', character == 'E':
			current.WriteRune(character)
		default:
			flush()
		}
	}
	flush()
	return numerals
}
