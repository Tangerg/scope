package pgfilter_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore/pgfilter"
)

func TestCompilerConformance(t *testing.T) {
	storetest.VisitorConformance(t, func(source string) error {
		predicate, err := filter.Parse(source)
		if err != nil {
			return err
		}
		return predicate.Accept(pgfilter.NewCompiler("metadata"))
	}, storetest.Options{})
}

func build(t *testing.T, source string) pgfilter.Query {
	t.Helper()
	predicate, err := filter.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	compiler := pgfilter.NewCompiler("metadata")
	if err := predicate.Accept(compiler); err != nil {
		t.Fatal(err)
	}
	return compiler.Result()
}

func TestCompilerParameterizedJSONB(t *testing.T) {
	for _, test := range []struct {
		source string
		want   string
		args   []any
	}{
		{`author == 'Alice'`, `COALESCE((metadata -> $1::text) = $2::jsonb, FALSE)`, []any{"author", `"Alice"`}},
		{`year == 2020`, `COALESCE((metadata -> $1::text) = $2::jsonb, FALSE)`, []any{"year", "2020"}},
		{`published == true`, `COALESCE((metadata -> $1::text) = $2::jsonb, FALSE)`, []any{"published", "true"}},
		{`author != 'Alice'`, `(NOT COALESCE((metadata -> $1::text) = $2::jsonb, FALSE))`, []any{"author", `"Alice"`}},
		{`not (author == 'Alice')`, `(NOT COALESCE((metadata -> $1::text) = $2::jsonb, FALSE))`, []any{"author", `"Alice"`}},
		{`author is null`, `COALESCE((metadata -> $1::text) = 'null'::jsonb, TRUE)`, []any{"author"}},
		{`author is not null`, `(NOT COALESCE((metadata -> $1::text) = 'null'::jsonb, TRUE))`, []any{"author"}},
		{`year in (2020, 2021)`, `COALESCE((metadata -> $1::text) IN ($2::jsonb, $3::jsonb), FALSE)`, []any{"year", "2020", "2021"}},
		{`tag not in ('rag', 'llm')`, `(NOT COALESCE((metadata -> $1::text) IN ($2::jsonb, $3::jsonb), FALSE))`, []any{"tag", `"rag"`, `"llm"`}},
		{`profile['tags'] has 'rag'`, `COALESCE(((metadata -> $1::text) -> $2::text) @> jsonb_build_array($3::jsonb), FALSE)`, []any{"profile", "tags", `"rag"`}},
		{`value == 18446744073709551615`, `COALESCE((metadata -> $1::text) = $2::jsonb, FALSE)`, []any{"value", "18446744073709551615"}},
		{`value in (-9223372036854775808, 18446744073709551615, 0.1)`, `COALESCE((metadata -> $1::text) IN ($2::jsonb, $3::jsonb, $4::jsonb), FALSE)`, []any{"value", "-9223372036854775808", "18446744073709551615", "0.1"}},
		{`profile['a']['b'] == 'x'`, `COALESCE((((metadata -> $1::text) -> $2::text) -> $3::text) = $4::jsonb, FALSE)`, []any{"profile", "a", "b", `"x"`}},
		{`value['a\\b']['quote\'key'] == 'x'`, `COALESCE((((metadata -> $1::text) -> $2::text) -> $3::text) = $4::jsonb, FALSE)`, []any{"value", `a\b`, "quote'key", `"x"`}},
	} {
		t.Run(test.source, func(t *testing.T) {
			got := build(t, test.source)
			want := pgfilter.Query{Predicate: test.want, Args: test.args}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("query = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCompilerTypeErrorsHaveSeparateNativeSelection(t *testing.T) {
	for _, test := range []struct {
		source    string
		condition string
		invalid   string
		args      []any
	}{
		{`year >= 2020`, `COALESCE((jsonb_typeof((metadata -> $1::text)) = 'number' AND (metadata -> $1::text) >= $2::jsonb), FALSE)`, `COALESCE(jsonb_typeof((metadata -> $1::text)) NOT IN ('number', 'null'), FALSE)`, []any{"year", "2020"}},
		{`author like 'a\\%b'`, `COALESCE((jsonb_typeof((metadata -> $1::text)) = 'string' AND ((metadata -> $1::text) #>> '{}') LIKE $2::text ESCAPE ''), FALSE)`, `COALESCE(jsonb_typeof((metadata -> $1::text)) NOT IN ('string', 'null'), FALSE)`, []any{"author", `a\%b`}},
	} {
		t.Run(test.source, func(t *testing.T) {
			got := build(t, test.source)
			want := pgfilter.Query{Predicate: "(" + test.condition + " AND NOT " + test.invalid + ")", Invalid: test.invalid, Args: test.args}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("query = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCompilerArrayIndicesRequireArrays(t *testing.T) {
	for _, test := range []struct {
		source string
		want   string
		args   []any
	}{
		{`tags[0] == 'a'`, `COALESCE((CASE WHEN jsonb_typeof((metadata -> $1::text)) = 'array' THEN ((metadata -> $1::text) -> 0) END) = $2::jsonb, FALSE)`, []any{"tags", `"a"`}},
		{`tags['0'] == 'a'`, `COALESCE(((metadata -> $1::text) -> $2::text) = $3::jsonb, FALSE)`, []any{"tags", "0", `"a"`}},
		{`items[2]['name'] == 'a'`, `COALESCE((CASE WHEN jsonb_typeof((metadata -> $1::text)) = 'array' THEN (((metadata -> $1::text) -> 2) -> $2::text) END) = $3::jsonb, FALSE)`, []any{"items", "name", `"a"`}},
		{`items[0][0] == 'a'`, `COALESCE((CASE WHEN jsonb_typeof((metadata -> $1::text)) = 'array' AND jsonb_typeof(((metadata -> $1::text) -> 0)) = 'array' THEN (((metadata -> $1::text) -> 0) -> 0) END) = $2::jsonb, FALSE)`, []any{"items", `"a"`}},
	} {
		t.Run(test.source, func(t *testing.T) {
			got := build(t, test.source)
			want := pgfilter.Query{Predicate: test.want, Args: test.args}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("query = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCompilerDeepArrayPathHasBoundedSQL(t *testing.T) {
	query := build(t, "items"+strings.Repeat("[0]", 16)+" == 1")
	if len(query.Predicate) > 64*1024 {
		t.Fatalf("a 16-index path expanded into %d SQL bytes", len(query.Predicate))
	}
}

func TestCompilerFailedVisitClearsAllOutput(t *testing.T) {
	compiler := pgfilter.NewCompiler("metadata")
	for _, source := range []string{`value > 1`, `value[2147483648] == 1`} {
		predicate, err := filter.Parse(source)
		if err != nil {
			t.Fatal(err)
		}
		err = predicate.Accept(compiler)
		if source == `value > 1` {
			if err != nil || compiler.Result().Invalid == "" {
				t.Fatalf("initial compilation = %#v, %v", compiler.Result(), err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "exceeds the jsonb integer operand range") {
			t.Fatalf("out-of-range array index = %v", err)
		}
	}
	if got := compiler.Result(); !reflect.DeepEqual(got, pgfilter.Query{}) {
		t.Fatalf("failed visit retained output: %#v", got)
	}
	if err := compiler.Visit(nil); err == nil {
		t.Fatal("nil predicate must fail")
	}
}

func TestCompilerRejectsNULBeforePublishingSQL(t *testing.T) {
	for _, predicate := range []filter.Predicate{
		filter.EQ("value", "\x00"),
		filter.Has("value", "\x00"),
		filter.In("value", []string{"safe", "\x00"}),
		filter.Like("value", "\x00%"),
		filter.IsNull(filter.Index("value", "\x00")),
	} {
		compiler := pgfilter.NewCompiler("facts")
		if err := predicate.Accept(compiler); err == nil {
			t.Fatalf("NUL-bearing predicate %s compiled", predicate)
		}
		if got := compiler.Result(); !reflect.DeepEqual(got, pgfilter.Query{}) {
			t.Fatalf("failed compilation published %#v", got)
		}
	}
}
