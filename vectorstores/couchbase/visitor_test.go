package couchbase

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// TestVisitor_Conformance exercises every AST shape the filter DSL
// supports against the couchbase visitor via the shared
// [storetest.VisitorConformance] suite. This is "no shape crashes"
// coverage; output equivalence stays in the per-test functions below.
func TestVisitor_Conformance(t *testing.T) {
	storetest.VisitorConformance(t, func(src string) error {
		expr, err := filter.Parse(src)
		if err != nil {
			return err
		}
		v := newVisitor("metadata")
		return expr.Accept(v)
	},
		storetest.Options{
			CompileText: compileFilterText,
		},
	)
}

// build is the test driver — parse src, visit, return (sql, err).
func build(t *testing.T, src string) (string, error) {
	t.Helper()
	expr, err := filter.Parse(src)
	if err != nil {
		return "", err
	}
	v := newVisitor("metadata")
	if err := expr.Accept(v); err != nil {
		return "", err
	}
	return v.snapshot(), nil
}

// SQL++ IS NULL requires an explicit NULL and does not match a MISSING path,
// so it would answer nothing for a document that simply lacks the key. IS NOT
// VALUED is true for both NULL and MISSING, which is how the AST reads an
// absent key.
func TestVisitor_IsNull(t *testing.T) {
	sql, err := build(t, `author is null`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "metadata.`author` IS NOT VALUED") {
		t.Fatalf("sql=%q must test metadata.`author` IS NOT VALUED", sql)
	}
	if strings.Contains(sql, "IS NULL") {
		t.Fatalf("sql=%q uses IS NULL, which does not match a MISSING path", sql)
	}
}

func TestVisitor_IsNotNull(t *testing.T) {
	sql, err := build(t, `author is not null`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// NOT(field IS NOT VALUED) — semantically IS VALUED.
	if !strings.Contains(sql, "NOT") || !strings.Contains(sql, "IS NOT VALUED") {
		t.Fatalf("sql=%q must wrap IS NOT VALUED in NOT", sql)
	}
}

// A comparison whose operand is MISSING yields MISSING in SQL++, which drops
// the document for any operator and stays MISSING under NOT. Each leaf carries
// the truth value the AST assigns an absent key instead.
func TestVisitor_LeavesAreTotalOverAnAbsentKey(t *testing.T) {
	for _, sample := range []struct {
		source string
		guard  string
	}{
		{source: `author == 'Alice'`, guard: "IS VALUED AND"},
		{source: `year > 2020`, guard: "IS VALUED AND"},
		{source: `author like 'A%'`, guard: "IS VALUED AND"},
		{source: `author in ('a','b')`, guard: "IS VALUED AND"},
		{source: `author != 'Alice'`, guard: "IS NOT VALUED OR"},
	} {
		sql, err := build(t, sample.source)
		if err != nil {
			t.Fatalf("build %q: %v", sample.source, err)
		}
		if !strings.Contains(sql, sample.guard) {
			t.Fatalf("sql=%q for %q must be guarded by %q", sql, sample.source, sample.guard)
		}
	}
}

func TestVisitor_CollectionMembership(t *testing.T) {
	sql, err := build(t, `visible_to has 'user-42'`)
	if err != nil {
		t.Fatal(err)
	}
	want := `ANY element IN metadata.` + "`visible_to`" + ` SATISFIES element = "user-42" END`
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

// compileFilterText drives the compiler and returns the query text it produced,
// so the shared suite can require the exact digits of a numeric literal. This
// compiler's whole output is text, which is what makes those digits the only
// thing between a caller's filter and a different one.
func compileFilterText(source string) (string, error) {
	expr, err := filter.Parse(source)
	if err != nil {
		return "", err
	}
	compiler := newVisitor("metadata")
	if err := expr.Accept(compiler); err != nil {
		return "", err
	}
	return compiler.snapshot(), nil
}
