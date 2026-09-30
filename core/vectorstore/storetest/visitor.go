package storetest

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// BuildFn must parse the source and compile it through the provider visitor.
type BuildFn func(source string) error

type Options struct {
	// Unsupported cases must return an error; approximation and skipping are forbidden.
	Unsupported []string

	// InterpolatesKeyPaths declares that keys are written as query syntax.
	// Such compilers must reject keys the language cannot name safely; compilers
	// that bind keys as values must preserve arbitrary keys.
	InterpolatesKeyPaths bool

	// Set CompileText when the emitted query is text so the suite can verify
	// numerals retain their exact value. Leave it nil for bound arguments or
	// provider structures; rendering those solely for this check would test a
	// representation the backend never receives.
	CompileText func(source string) (string, error)

	// NumericDomainIsFloat64 requires equal float64 values instead of equal
	// integer digits, matching the precision of providers that store doubles.
	NumericDomainIsFloat64 bool
}

func VisitorConformance(t *testing.T, build BuildFn, options Options) {
	t.Helper()

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

		{"null_test", `author is null`},
		{"not_null_test", `author is not null`},
	}
	for _, tc := range success {
		t.Run("Success_"+tc.name, func(t *testing.T) {
			unsupported := slices.Contains(options.Unsupported, tc.name)
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
	}{
		{"like_number", `title like 42`},
	}
	for _, tc := range failure {
		t.Run("Failure_"+tc.name, func(t *testing.T) {
			if err := build(tc.src); err == nil {
				t.Fatalf("expected error on %q, got nil", tc.src)
			}
		})
	}

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
			if options.InterpolatesKeyPaths {
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

	if options.CompileText != nil {
		runNumeralCases(t, options.CompileText, options.NumericDomainIsFloat64)
	}
}

// These magnitudes expose lossy float64 round trips and architecture-dependent
// out-of-range float-to-int conversions.
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
				// A compiler may reject an unrepresentable magnitude, but must never round it.
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

// Query syntax is provider-owned; scan loosely and let ParseFloat validate candidates.
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
