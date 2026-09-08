package filter

import (
	"math"
	"strings"
	"testing"
)

func mustParse(t *testing.T, source string) Predicate {
	t.Helper()
	predicate, err := Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	return predicate
}

func mustMatch(t *testing.T, predicate Predicate, metadata map[string]any) bool {
	t.Helper()
	matched, err := Match(predicate, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return matched
}

// TestEvaluatorOperatorSemantics pins the truth table the DSL promises. A
// backend compiler is validated against the same predicates, so any drift here
// silently makes in-memory results disagree with a real store.
func TestEvaluatorOperatorSemantics(t *testing.T) {
	metadata := map[string]any{
		"category": "tech",
		"year":     int64(2020),
		"ratio":    0.5,
		"tags":     []any{"go", "ai"},
		"nested":   map[string]any{"list": []any{int64(10), int64(20)}},
		"absent":   nil,
	}
	cases := map[string]struct {
		source string
		want   bool
	}{
		"equal true":            {source: `category == 'tech'`, want: true},
		"equal false":           {source: `category == 'other'`, want: false},
		"not equal":             {source: `category != 'other'`, want: true},
		"less":                  {source: `year < 2021`, want: true},
		"less equal":            {source: `year <= 2020`, want: true},
		"greater":               {source: `year > 2019`, want: true},
		"greater equal":         {source: `year >= 2020`, want: true},
		"greater false":         {source: `year > 2021`, want: false},
		"less false":            {source: `year < 2019`, want: false},
		"less equal false":      {source: `year <= 2019`, want: false},
		"greater equal false":   {source: `year >= 2021`, want: false},
		"mixed integer float":   {source: `ratio < 1`, want: true},
		"in list":               {source: `category in ('tech','news')`, want: true},
		"not in list":           {source: `category in ('news')`, want: false},
		"negated in list":       {source: `category not in ('news')`, want: true},
		"has element":           {source: `tags has 'go'`, want: true},
		"has missing element":   {source: `tags has 'rust'`, want: false},
		"like prefix":           {source: `category like 'te%'`, want: true},
		"like single":           {source: `category like 'tec_'`, want: true},
		"like no match":         {source: `category like 'x%'`, want: false},
		"is null":               {source: `absent is null`, want: true},
		"is null on missing":    {source: `missing is null`, want: true},
		"is not null":           {source: `category is not null`, want: true},
		"and both true":         {source: `category == 'tech' and year == 2020`, want: true},
		"and short circuit":     {source: `category == 'other' and year == 2020`, want: false},
		"or short circuit":      {source: `category == 'tech' or year == 1999`, want: true},
		"or both false":         {source: `category == 'x' or year == 1999`, want: false},
		"not":                   {source: `not (category == 'other')`, want: true},
		"index chain":           {source: `nested['list'][1] == 20`, want: true},
		"index out of range":    {source: `nested['list'][9] is null`, want: true},
		"index into scalar":     {source: `category['x'] is null`, want: true},
		"missing key in map":    {source: `nested['absent'] is null`, want: true},
		"ordering absent field": {source: `missing > 1`, want: false},
		"like absent field":     {source: `missing like '%'`, want: false},
		"has absent field":      {source: `missing has 'x'`, want: false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mustMatch(t, mustParse(t, testCase.source), metadata); got != testCase.want {
				t.Fatalf("%s = %t, want %t", testCase.source, got, testCase.want)
			}
		})
	}
}

// TestEvaluatorReportsMalformedPredicates keeps a type-confused filter loud. A
// silent false would make a store quietly return the wrong document set.
func TestEvaluatorReportsMalformedPredicates(t *testing.T) {
	metadata := map[string]any{
		"category": "tech",
		"year":     int64(2020),
		"tags":     []any{"go"},
		"nested":   map[string]any{"list": []any{int64(1)}},
	}
	cases := map[string]struct {
		predicate Predicate
		wantText  string
	}{
		"ordering on string": {
			predicate: mustParse(t, `category > 1`),
			wantText:  "left operand must be numeric",
		},
		"like on number": {
			predicate: mustParse(t, `year like 'a%'`),
			wantText:  "LIKE left operand must be string",
		},
		"string array index": {
			predicate: mustParse(t, `nested['list']['zero'] == 1`),
			wantText:  "invalid array index",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Match(testCase.predicate, metadata)
			if err == nil {
				t.Fatal("malformed predicate evaluated without error")
			}
			if !strings.Contains(err.Error(), testCase.wantText) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.wantText)
			}
		})
	}
}

// TestEvaluatorComparesAcrossIntegerSignedness covers the mixed-signedness
// path: collapsing through float64 would make values near the 64-bit limits
// compare equal when they are not.
func TestEvaluatorComparesAcrossIntegerSignedness(t *testing.T) {
	cases := map[string]struct {
		stored any
		source string
		want   bool
	}{
		"unsigned stored, signed literal greater":  {stored: uint64(10), source: `n > 9`, want: true},
		"unsigned stored, negative literal":        {stored: uint64(0), source: `n > -1`, want: true},
		"signed negative stored, unsigned compare": {stored: int64(-1), source: `n < 18446744073709551615`, want: true},
		"max uint64 not equal to max int64":        {stored: uint64(math.MaxUint64), source: `n == 9223372036854775807`, want: false},
		"int32 stored":                             {stored: int32(5), source: `n == 5`, want: true},
		"uint8 stored":                             {stored: uint8(5), source: `n == 5`, want: true},
		"float stored equals integer":              {stored: 5.0, source: `n == 5`, want: true},
		"float32 stored":                           {stored: float32(2.5), source: `n > 2`, want: true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mustMatch(t, mustParse(t, testCase.source), map[string]any{"n": testCase.stored}); got != testCase.want {
				t.Fatalf("%s with %v (%T) = %t, want %t", testCase.source, testCase.stored, testCase.stored, got, testCase.want)
			}
		})
	}
}

// TestEvaluatorAcceptsEveryGoNumericWidth keeps metadata decoded by a backend
// comparable regardless of which concrete Go numeric type it arrived as. Both
// the exact integer path and the mixed float fallback must agree.
func TestEvaluatorAcceptsEveryGoNumericWidth(t *testing.T) {
	widths := map[string]any{
		"int":     int(5),
		"int8":    int8(5),
		"int16":   int16(5),
		"int32":   int32(5),
		"int64":   int64(5),
		"uint":    uint(5),
		"uint8":   uint8(5),
		"uint16":  uint16(5),
		"uint32":  uint32(5),
		"uint64":  uint64(5),
		"float32": float32(5),
		"float64": float64(5),
	}
	for name, value := range widths {
		t.Run(name, func(t *testing.T) {
			metadata := map[string]any{"n": value}
			if !mustMatch(t, mustParse(t, `n == 5`), metadata) {
				t.Error("integer comparison did not match")
			}
			if !mustMatch(t, mustParse(t, `n > 4.5`), metadata) {
				t.Error("mixed float comparison did not match")
			}
			if !mustMatch(t, mustParse(t, `n < 5.5`), metadata) {
				t.Error("mixed float comparison did not order correctly")
			}
		})
	}
}

// TestEvaluatorTreatsNaNAsUnordered keeps NaN out of the ordering result rather
// than letting it satisfy both `<` and `>=`.
func TestEvaluatorTreatsNaNAsUnordered(t *testing.T) {
	metadata := map[string]any{"n": math.NaN()}
	for _, source := range []string{`n < 1`, `n > 1`, `n <= 1`, `n >= 1`, `n == 1`} {
		if mustMatch(t, mustParse(t, source), metadata) {
			t.Errorf("%s matched a NaN value", source)
		}
	}
}

// TestEvaluatorRejectsNumericStringCoercion documents the deliberate refusal to
// coerce: "12" < "9" is a caller mistake, not a numeric comparison.
func TestEvaluatorRejectsNumericStringCoercion(t *testing.T) {
	if _, err := Match(mustParse(t, `n < 9`), map[string]any{"n": "12"}); err == nil {
		t.Fatal("numeric string was silently coerced")
	}
}

// TestEvaluatorArrayIndexBounds pins which index values address an array. A
// fractional or negative index is a filter bug, while an out-of-range one is
// simply absent.
func TestEvaluatorArrayIndexBounds(t *testing.T) {
	metadata := map[string]any{"list": []any{int64(1), int64(2)}}
	if !mustMatch(t, EQ(Index("list", 1), 2), metadata) {
		t.Fatal("integer index did not address the array")
	}
	if !mustMatch(t, IsNull(Index("list", 5)), metadata) {
		t.Fatal("out-of-range index was not treated as absent")
	}
	for name, index := range map[string]float64{
		"negative":   -1,
		"fractional": 1.5,
		"too large":  1 << 63,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Match(EQ(Index("list", index), 2), metadata); err == nil {
				t.Fatalf("index %v was accepted", index)
			}
		})
	}
}

// TestEvaluatorHandlesNilMetadata proves a document with no metadata evaluates
// as all-absent instead of panicking on a nil map.
func TestEvaluatorHandlesNilMetadata(t *testing.T) {
	if !mustMatch(t, mustParse(t, `anything is null`), nil) {
		t.Fatal("nil metadata did not read as absent")
	}
	if mustMatch(t, mustParse(t, `anything == 'x'`), nil) {
		t.Fatal("nil metadata matched an equality predicate")
	}
}

// TestEvaluatorHasRequiresACollection keeps HAS from silently succeeding on a
// scalar that happens to equal the wanted value.
func TestEvaluatorHasRequiresACollection(t *testing.T) {
	if mustMatch(t, mustParse(t, `scalar has 'x'`), map[string]any{"scalar": "x"}) {
		t.Fatal("HAS matched a scalar field")
	}
	if !mustMatch(t, Has("numbers", 2), map[string]any{"numbers": []any{int64(1), int64(2)}}) {
		t.Fatal("HAS did not match a numeric element")
	}
}

// TestLikeMatchPatterns exercises the backtracking matcher directly, including
// the wildcard-restart path a whole-input match depends on.
func TestLikeMatchPatterns(t *testing.T) {
	cases := map[string]struct {
		input   string
		pattern string
		want    bool
	}{
		"exact":                {input: "scope", pattern: "scope", want: true},
		"prefix wildcard":      {input: "scope", pattern: "%pe", want: true},
		"suffix wildcard":      {input: "scope", pattern: "sc%", want: true},
		"inner wildcard":       {input: "scope", pattern: "s%e", want: true},
		"single char":          {input: "scope", pattern: "sco_e", want: true},
		"single char short":    {input: "scope", pattern: "sco_", want: false},
		"trailing wildcards":   {input: "scope", pattern: "scope%%", want: true},
		"only wildcard":        {input: "scope", pattern: "%", want: true},
		"empty input wildcard": {input: "", pattern: "%", want: true},
		"empty input exact":    {input: "", pattern: "", want: true},
		"empty pattern":        {input: "scope", pattern: "", want: false},
		"needs backtracking":   {input: "aaa", pattern: "%a", want: true},
		"backtrack fails":      {input: "aaa", pattern: "%b", want: false},
		"unicode":              {input: "范围", pattern: "范_", want: true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := likeMatch(testCase.input, testCase.pattern); got != testCase.want {
				t.Fatalf("likeMatch(%q, %q) = %t, want %t", testCase.input, testCase.pattern, got, testCase.want)
			}
		})
	}
}

// TestEvaluatorInRequiresAList keeps IN from accepting a scalar right operand
// that a provider compiler would have rejected.
func TestEvaluatorInRequiresAList(t *testing.T) {
	metadata := map[string]any{"category": "tech"}
	if !mustMatch(t, In("category", []string{"tech", "news"}), metadata) {
		t.Fatal("IN did not match a listed value")
	}
	if mustMatch(t, In("category", []string{"news"}), metadata) {
		t.Fatal("IN matched an unlisted value")
	}
	if mustMatch(t, In("missing", []string{"tech"}), metadata) {
		t.Fatal("IN matched an absent field")
	}
}
