package pgfilter_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore/pgfilter"
)

// TestCompiler_Conformance exercises every AST shape the filter DSL
// supports against the PostgreSQL compiler via the shared
// [storetest.VisitorConformance] suite. Output equivalence stays in
// the per-test functions below; this is "no shape crashes" coverage.
func TestCompiler_Conformance(t *testing.T) {
	storetest.VisitorConformance(t, func(src string) error {
		expr, err := filter.Parse(src)
		if err != nil {
			return err
		}
		compiler := pgfilter.NewCompiler("metadata")
		return expr.Accept(compiler)
	})
}

// build is the test driver — parse src, visit, return (sql, args, err).
func build(t *testing.T, src string) (string, []any, error) {
	t.Helper()
	expr, err := filter.Parse(src)
	if err != nil {
		return "", nil, err
	}
	compiler := pgfilter.NewCompiler("metadata")
	if err := expr.Accept(compiler); err != nil {
		return "", nil, err
	}
	sql, args := compiler.Result()
	return sql, args, nil
}

func TestCompiler_EqualityString(t *testing.T) {
	sql, args, err := build(t, `author == 'Alice'`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "metadata->>'author'") {
		t.Fatalf("sql=%q must address metadata->>'author'", sql)
	}
	if !strings.Contains(sql, "= $1") {
		t.Fatalf("sql=%q must contain '= $1'", sql)
	}
	if !reflect.DeepEqual(args, []any{"Alice"}) {
		t.Fatalf("args=%v, want [Alice]", args)
	}
}

func TestCompiler_EqualityNumberCastsNumeric(t *testing.T) {
	sql, args, err := build(t, `year == 2020`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "::numeric") {
		t.Fatalf("sql=%q must contain ::numeric cast for number compare", sql)
	}
	// Whole numbers come back as int64 from the literal converter.
	if !reflect.DeepEqual(args, []any{int64(2020)}) {
		t.Fatalf("args=%v, want [int64(2020)]", args)
	}
}

func TestCompiler_EqualityBoolCastsBoolean(t *testing.T) {
	sql, args, err := build(t, `published == true`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "::boolean") {
		t.Fatalf("sql=%q must contain ::boolean cast for bool compare", sql)
	}
	if !reflect.DeepEqual(args, []any{true}) {
		t.Fatalf("args=%v, want [true]", args)
	}
}

func TestCompiler_Ordering(t *testing.T) {
	sql, args, err := build(t, `year >= 2020`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, ">= $1") {
		t.Fatalf("sql=%q must contain '>= $1'", sql)
	}
	if !reflect.DeepEqual(args, []any{int64(2020)}) {
		t.Fatalf("args=%v, want [int64(2020)]", args)
	}
}

func TestCompiler_LogicalAnd(t *testing.T) {
	sql, args, err := build(t, `author == 'Alice' and year >= 2020`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, " AND ") {
		t.Fatalf("sql=%q must contain ' AND '", sql)
	}
	if len(args) != 2 {
		t.Fatalf("args=%v, want 2 placeholders", args)
	}
	// Placeholders must be numbered sequentially.
	if !strings.Contains(sql, "$1") || !strings.Contains(sql, "$2") {
		t.Fatalf("sql=%q must contain $1 and $2", sql)
	}
}

func TestCompiler_LogicalOr(t *testing.T) {
	sql, _, err := build(t, `a == 1 or b == 2`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, " OR ") {
		t.Fatalf("sql=%q must contain ' OR '", sql)
	}
}

func TestCompiler_Not(t *testing.T) {
	sql, _, err := build(t, `not (a == 1)`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "(NOT ") {
		t.Fatalf("sql=%q must contain '(NOT '", sql)
	}
}

func TestCompiler_InStrings(t *testing.T) {
	sql, args, err := build(t, `tag in ('rag', 'llm')`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "= ANY($1)") {
		t.Fatalf("sql=%q must contain '= ANY($1)'", sql)
	}
	if !reflect.DeepEqual(args, []any{[]string{"rag", "llm"}}) {
		t.Fatalf("args=%v, want [[rag llm]]", args)
	}
}

func TestCompiler_InNumbers(t *testing.T) {
	sql, args, err := build(t, `year in (2020, 2021, 2022)`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "::numeric") {
		t.Fatalf("sql=%q must cast left side to ::numeric for numeric IN", sql)
	}
	want := []any{[]int64{2020, 2021, 2022}}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%v, want %v", args, want)
	}
}

func TestCompiler_HasUsesJSONBCollectionContainment(t *testing.T) {
	sql, args, err := build(t, `profile['tags'] has 'rag'`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	wantSQL := `(metadata->'profile'->'tags' @> jsonb_build_array($1))`
	if sql != wantSQL {
		t.Fatalf("sql = %q, want %q", sql, wantSQL)
	}
	if !reflect.DeepEqual(args, []any{"rag"}) {
		t.Fatalf("args = %v, want [rag]", args)
	}
}

// LIKE is case-sensitive, which is why Postgres ships ILIKE separately.
// Emitting ILIKE would answer a wider question than the filter asked.
func TestCompiler_Like(t *testing.T) {
	sql, args, err := build(t, `author like '%Alice%'`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, " LIKE $1") {
		t.Fatalf("sql=%q must contain ' LIKE $1'", sql)
	}
	if strings.Contains(sql, "ILIKE") {
		t.Fatalf("sql=%q widens the match to case-insensitive", sql)
	}
	if !reflect.DeepEqual(args, []any{"%Alice%"}) {
		t.Fatalf("args=%v, want [%%Alice%%]", args)
	}
}

func TestCompiler_NestedIndex(t *testing.T) {
	sql, _, err := build(t, `profile['a']['b'] == 'x'`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Nested: intermediate hops use ->, final step uses ->>.
	if !strings.Contains(sql, "metadata->'profile'->'a'->>'b'") {
		t.Fatalf("sql=%q must contain metadata->'profile'->'a'->>'b'", sql)
	}
}

func TestCompiler_IndexedKeyRetainsBase(t *testing.T) {
	sql, _, err := build(t, `profile['author'] == 'Alice'`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "metadata->'profile'->>'author'") {
		t.Fatalf("sql=%q must contain metadata->'profile'->>'author'", sql)
	}
}

func TestCompiler_EmptyMetadataColDefaults(t *testing.T) {
	expr, err := filter.Parse(`a == 1`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	compiler := pgfilter.NewCompiler("") // empty → defaults to "metadata"
	if err := expr.Accept(compiler); err != nil {
		t.Fatalf("visit: %v", err)
	}
	sql, _ := compiler.Result()
	if !strings.Contains(sql, "metadata->>'a'") {
		t.Fatalf("sql=%q must default to metadata col", sql)
	}
}

func TestCompiler_NilExpression(t *testing.T) {
	compiler := pgfilter.NewCompiler("metadata")
	if compiler.Visit(nil) == nil {
		t.Fatal("nil expression must produce an error")
	}
}

func TestCompiler_IsNull(t *testing.T) {
	sql, args, err := build(t, `author is null`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "metadata->>'author'") || !strings.Contains(sql, "IS NULL") {
		t.Fatalf("sql=%q must contain metadata->>'author' IS NULL", sql)
	}
	if len(args) != 0 {
		t.Fatalf("IS NULL takes no bound args, got %v", args)
	}
}

func TestCompiler_IsNotNull(t *testing.T) {
	sql, _, err := build(t, `author is not null`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// NOT(field IS NULL) — semantically IS NOT NULL.
	if !strings.Contains(sql, "NOT") || !strings.Contains(sql, "IS NULL") {
		t.Fatalf("sql=%q must wrap IS NULL in NOT", sql)
	}
}

func TestCompiler_NotIn(t *testing.T) {
	sql, args, err := build(t, `tags not in ('a', 'b')`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// NOT(tags IN (...)) — the existing NOT wrapper around = ANY.
	if !strings.Contains(sql, "NOT") || !strings.Contains(sql, "ANY") {
		t.Fatalf("sql=%q must wrap an = ANY(...) in NOT", sql)
	}
	if len(args) != 1 {
		t.Fatalf("expected one array arg, got %v", args)
	}
}
