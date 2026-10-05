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
		// Native operands are encoded strings, not numerals. Differential
		// projection tests verify their exact values and order instead.
		storetest.Options{},
	)
}

func TestVisitor_PreservesLargeIntegerText(t *testing.T) {
	visitor := newVisitor()
	if err := filter.EQ("id", uint64(math.MaxUint64)).Accept(visitor); err != nil {
		t.Fatal(err)
	}
	actual := visitor.snapshot()
	if actual != `(ARRAY_CONTAINS(metadata_filter["kinds"]["number"], "[\"id\"]") and metadata_filter["scalars"]["[\"id\"]"] == "number:20922337203685477582718446744073709551615/")` {
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
	if got := v.snapshot(); got != `(ARRAY_CONTAINS(metadata_filter["kinds"]["array"], "[\"tags\"]") and ARRAY_CONTAINS(metadata_filter["members"]["[\"tags\"]"], "string:[000072][000061][000067]"))` {
		t.Fatalf("Result() = %q", got)
	}
}

func TestVisitor_QuotesCompleteStringLiteral(t *testing.T) {
	value := "line one\nline two\\path\"quoted"
	visitor := newVisitor()
	if err := filter.EQ("value", value).Accept(visitor); err != nil {
		t.Fatal(err)
	}
	if got, want := visitor.snapshot(), `(ARRAY_CONTAINS(metadata_filter["kinds"]["string"], "[\"value\"]") and metadata_filter["scalars"]["[\"value\"]"] == `+strconv.Quote("string:"+encodeMetadataString(value, false))+`)`; got != want {
		t.Fatalf("Result() = %q, want %q", got, want)
	}
}

func TestVisitor_SelectorKeepsSegmentKinds(t *testing.T) {
	for source, want := range map[string]string{
		`meta[0] == 'a'`:           `(ARRAY_CONTAINS(metadata_filter["kinds"]["string"], "[\"meta\",0]") and metadata_filter["scalars"]["[\"meta\",0]"] == "string:[000061]")`,
		`meta['0'] == 'a'`:         `(ARRAY_CONTAINS(metadata_filter["kinds"]["string"], "[\"meta\",\"0\"]") and metadata_filter["scalars"]["[\"meta\",\"0\"]"] == "string:[000061]")`,
		`meta['a'][1]['b'] == 'x'`: `(ARRAY_CONTAINS(metadata_filter["kinds"]["string"], "[\"meta\",\"a\",1,\"b\"]") and metadata_filter["scalars"]["[\"meta\",\"a\",1,\"b\"]"] == "string:[000078]")`,
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
		condition := `(ARRAY_CONTAINS(metadata_filter["kinds"]["string"], "[\"author\"]") and metadata_filter["scalars"]["[\"author\"]"] == ` + strconv.Quote("string:"+encodeMetadataString(pattern, false)) + `)`
		invalid := `(ARRAY_CONTAINS(metadata_filter["present"], "[\"author\"]") and not (ARRAY_CONTAINS(metadata_filter["kinds"]["string"], "[\"author\"]")))`
		if got, want := compiler.snapshot(), "("+condition+") and not ("+invalid+")"; got != want {
			t.Fatalf("LIKE %q = %q, want %q", pattern, got, want)
		}
	}
}

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
