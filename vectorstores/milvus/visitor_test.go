package milvus

import (
	"math"
	"strconv"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestVisitor_Conformance(t *testing.T) {
	storetest.VisitorConformance(t, func(src string) error {
		expr, err := filter.Parse(src)
		if err != nil {
			return err
		}
		v := newVisitor()
		return expr.Accept(v)
	},
		storetest.Options{
			// Native JSON-key null tests can classify empty containers as
			// absent, which changes Core's metadata semantics.
			Unsupported: []string{"null_test", "not_null_test"},
			CompileText: compileFilterText,
		},
	)
}

func TestVisitor_PreservesLargeIntegerText(t *testing.T) {
	visitor := newVisitor()
	if err := filter.EQ("id", uint64(math.MaxUint64)).Accept(visitor); err != nil {
		t.Fatal(err)
	}
	actual := visitor.snapshot()
	if actual != `metadata["id"] == 18446744073709551615` {
		t.Fatalf("filter = %q", actual)
	}
}

func TestVisitor_HasUsesArrayContains(t *testing.T) {
	expr, err := filter.Parse(`tags has 'rag'`)
	if err != nil {
		t.Fatal(err)
	}
	v := newVisitor()
	if err := expr.Accept(v); err != nil {
		t.Fatal(err)
	}
	if got := v.snapshot(); got != `ARRAY_CONTAINS(metadata["tags"], "rag")` {
		t.Fatalf("Result() = %q", got)
	}
}

func TestVisitor_QuotesCompleteStringLiteral(t *testing.T) {
	value := "line one\nline two\\path\"quoted"
	visitor := newVisitor()
	if err := filter.EQ("value", value).Accept(visitor); err != nil {
		t.Fatal(err)
	}
	if got, want := visitor.snapshot(), `metadata["value"] == `+strconv.Quote(value); got != want {
		t.Fatalf("Result() = %q, want %q", got, want)
	}
}

func TestVisitor_SelectorKeepsSegmentKinds(t *testing.T) {
	for source, want := range map[string]string{
		`meta[0] == 'a'`:           `metadata["meta"][0] == "a"`,
		`meta['0'] == 'a'`:         `metadata["meta"]["0"] == "a"`,
		`meta['a'][1]['b'] == 'x'`: `metadata["meta"]["a"][1]["b"] == "x"`,
	} {
		got, err := compileFilterText(source)
		if err != nil {
			t.Fatalf("compile %q: %v", source, err)
		}
		if got != want {
			t.Fatalf("compile %q = %q, want %q", source, got, want)
		}
	}
}

func TestLiteralLikePreservesBackslashesAndUnicode(t *testing.T) {
	for _, pattern := range []string{`plain\path`, "世界", "", "a[b]"} {
		compiler := newVisitor()
		if err := filter.Like("author", pattern).Accept(compiler); err != nil {
			t.Fatal(err)
		}
		if got, want := compiler.snapshot(), `metadata["author"] == `+strconv.Quote(pattern); got != want {
			t.Fatalf("LIKE %q = %q, want %q", pattern, got, want)
		}
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
	compiler := newVisitor()
	if err := expr.Accept(compiler); err != nil {
		return "", err
	}
	return compiler.snapshot(), nil
}
