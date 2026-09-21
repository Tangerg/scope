package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// FilterConfig connects the semantic corpus to a backend's query boundary.
type FilterConfig struct {
	// Query installs exactly documents in an isolated fixture, executes predicate
	// through the backend, and returns all selected document IDs. It must not
	// evaluate predicate with filter.Match or substitute expected responses.
	// The caller owns fixture cleanup and provider visibility synchronization.
	// Documents have UUID IDs and identical text; search adapters must request
	// at least len(documents) results without a relevance threshold.
	Query func(context.Context, []*document.Document, filter.Predicate) ([]string, error)

	// Unsupported maps corpus case names to the error identifying the backend's
	// explicit refusal. Each case must return that error via errors.Is and no
	// IDs. Unknown names and nil errors fail; transport errors cannot stand in
	// for unsupported semantics.
	Unsupported map[string]error
}

// FilterConformance compares backend selection with filter.Match and exact
// expected IDs. It covers every operator, null/missing fields, and LIKE's case,
// whole-value and Unicode wildcard semantics. Unlike Run, it executes queries
// and requires isolated fixtures; it never opens a provider connection itself.
func FilterConformance(t *testing.T, config FilterConfig) {
	t.Helper()
	if config.Query == nil {
		t.Fatal("storetest.FilterConformance: query is nil")
	}
	cases := filterCases()
	for name, err := range config.Unsupported {
		if lo.IsNil(err) || !slices.ContainsFunc(cases, func(c filterCase) bool { return c.name == name }) {
			t.Fatalf("storetest.FilterConformance: invalid unsupported case %q: %v", name, err)
		}
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			predicate, err := filter.Parse(test.source)
			if err != nil {
				t.Fatal(err)
			}
			docs := test.documents(t)
			var reference, want []string
			for _, index := range test.want {
				want = append(want, docs[index].ID)
			}
			for _, doc := range docs {
				values, valuesErr := doc.Metadata.Values()
				if valuesErr != nil {
					t.Fatal(valuesErr)
				}
				match, matchErr := filter.Match(predicate, values)
				if matchErr != nil {
					t.Fatal(matchErr)
				}
				if match {
					reference = append(reference, doc.ID)
				}
			}
			if !slices.Equal(reference, want) {
				t.Fatalf("filter.Match(%s) selected %v, want %v", predicate, reference, want)
			}
			got, err := config.Query(t.Context(), docs, predicate)
			if unsupported, exists := config.Unsupported[test.name]; exists {
				if !errors.Is(err, unsupported) || len(got) != 0 {
					t.Fatalf("unsupported query selected %v, error %v; want no IDs and %v", got, err, unsupported)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			if !slices.Equal(got, reference) {
				t.Errorf("backend query %s selected %v, reference selected %v", predicate, got, reference)
			}
		})
	}
}

type filterCase struct {
	name   string
	source string
	values []map[string]any
	want   []int
}

func (f filterCase) documents(t *testing.T) []*document.Document {
	t.Helper()
	docs := make([]*document.Document, len(f.values))
	for index, values := range f.values {
		encoded, err := metadata.FromValues(values)
		if err != nil {
			t.Fatal(err)
		}
		docs[index] = &document.Document{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1), Text: "filter conformance", Metadata: encoded,
		}
	}
	return docs
}

func filterCases() []filterCase {
	numbers := filterValues(9, 10, 11)
	strings := filterValues("Alice", "alice", "Bob")
	nullable := []map[string]any{{}, {"value": nil}, {"value": "present"}}
	return []filterCase{
		{name: "equal_decimal", source: `value == 0.1`, values: filterValues(0.1, 0.2, 0.3), want: []int{0}},
		{name: "decimal_ordering", source: `value < 0.2`, values: filterValues(-0.1, 0.1, 0.2, 0.3), want: []int{0, 1}},
		{name: "decimal_membership", source: `value in (0.1, 0.3)`, values: filterValues(0.1, 0.2, 0.3), want: []int{0, 2}},
		{name: "decimal_collection", source: `value has 0.1`, values: filterValues([]float64{0.1, 0.2}, []float64{0.2}, []float64{}), want: []int{0}},
		{name: "negated_ordering_null", source: `not (value < 5)`, values: []map[string]any{{}, {"value": nil}, {"value": 4}, {"value": 5}, {"value": 6}}, want: []int{0, 1, 3, 4}},
		{name: "equal_string", source: `value == 'Alice'`, values: strings, want: []int{0}},
		{name: "equal_number", source: `value == 10`, values: numbers, want: []int{1}},
		{name: "equal_bool", source: `value == true`, values: filterValues(true, false), want: []int{0}},
		{name: "not_equal", source: `value != 'Alice'`, values: strings, want: []int{1, 2}},
		{name: "less", source: `value < 10`, values: numbers, want: []int{0}},
		{name: "less_equal", source: `value <= 10`, values: numbers, want: []int{0, 1}},
		{name: "greater", source: `value > 10`, values: numbers, want: []int{2}},
		{name: "greater_equal", source: `value >= 10`, values: numbers, want: []int{1, 2}},
		{name: "and", source: `value >= 10 and value < 11`, values: numbers, want: []int{1}},
		{name: "or", source: `value < 10 or value > 10`, values: numbers, want: []int{0, 2}},
		{name: "not", source: `not (value == 10)`, values: numbers, want: []int{0, 2}},
		{name: "nested_logical", source: `(value < 10 or value == 10) and not (value == 9)`, values: numbers, want: []int{1}},
		{name: "in", source: `value in ('Alice', 'Bob')`, values: strings, want: []int{0, 2}},
		{name: "has", source: `value has 'a'`, values: filterValues([]string{"a", "b"}, []string{"b"}, []string{}), want: []int{0}},
		{name: "like_case", source: `value like '%foo%'`, values: filterValues("afoob", "aFOOb", "bar"), want: []int{0}},
		{name: "like_whole_value", source: `value like 'foo'`, values: filterValues("foo", "afoo", "foob"), want: []int{0}},
		{name: "like_percent", source: `value like 'f%o'`, values: filterValues("fo", "foo", "f\no", "of"), want: []int{0, 1, 2}},
		{name: "like_unicode_rune", source: `value like 'f_o'`, values: filterValues("féo", "fo", "f🙂o", "feeo"), want: []int{0, 2}},
		{name: "is_null", source: `value is null`, values: nullable, want: []int{0, 1}},
		{name: "is_not_null", source: `value is not null`, values: nullable, want: []int{2}},
		{name: "missing_equality", source: `value == 'present'`, values: nullable, want: []int{2}},
		{name: "missing_inequality", source: `value != 'present'`, values: nullable, want: []int{0, 1}},
		{name: "nested_key", source: `value['name'] == 'Alice'`, values: filterValues(map[string]any{"name": "Alice"}, map[string]any{"name": "Bob"}, map[string]any{}), want: []int{0}},
		{name: "no_matches", source: `value == 100`, values: numbers},
	}
}

func filterValues(values ...any) []map[string]any {
	rows := make([]map[string]any, len(values))
	for index, value := range values {
		rows[index] = map[string]any{"value": value}
	}
	return rows
}
