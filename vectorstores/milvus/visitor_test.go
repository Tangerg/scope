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
			// Milvus' expression syntax documents no IS NULL and no way to
			// test whether a JSON key is present, so a null test is refused
			// rather than approximated.
			Unsupported: []string{"null_test", "not_null_test"},
		},
	)
}

func TestVisitor_PreservesLargeIntegerText(t *testing.T) {
	visitor := newVisitor()
	if err := filter.EQ("id", uint64(math.MaxUint64)).Accept(visitor); err != nil {
		t.Fatal(err)
	}
	actual := visitor.snapshot()
	if actual != "id == 18446744073709551615" {
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
	if got := v.snapshot(); got != `ARRAY_CONTAINS(tags, "rag")` {
		t.Fatalf("Result() = %q", got)
	}
}

func TestVisitor_QuotesCompleteStringLiteral(t *testing.T) {
	value := "line one\nline two\\path\"quoted"
	visitor := newVisitor()
	if err := filter.EQ("value", value).Accept(visitor); err != nil {
		t.Fatal(err)
	}
	if got, want := visitor.snapshot(), "value == "+strconv.Quote(value); got != want {
		t.Fatalf("Result() = %q, want %q", got, want)
	}
}
