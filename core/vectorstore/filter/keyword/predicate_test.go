package keyword_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

func TestNativeTermsRejectLossyPredicates(t *testing.T) {
	got, err := keyword.EqualString(filter.EQ("code", "001"))
	if err != nil || got != keyword.String("code", "001") {
		t.Fatal(got, err)
	}
	for _, p := range []filter.Predicate{filter.EQ("code", 1), filter.GT("code", 1), filter.IsNull("code")} {
		term, err := keyword.EqualString(p)
		if term != "" || !errors.Is(err, vectorstore.ErrUnsupportedFilter) {
			t.Fatalf("%v: %s %v", p, term, err)
		}
	}
	if _, err := keyword.EqualString(nil); !errors.Is(err, vectorstore.ErrMissingFilter) {
		t.Fatal(err)
	}
	if terms, err := keyword.Project(metadata.Map{"bad": json.RawMessage("broken")}); terms != nil || err == nil {
		t.Fatal("admitted corrupt raw metadata")
	}
}

func TestLowerKeepsOnlyExactTermConditions(t *testing.T) {
	for _, test := range []struct {
		comparison *filter.BinaryExpr
		want       keyword.Condition
	}{
		{filter.EQ("code", "001"), keyword.Condition{Term: keyword.String("code", "001")}},
		{filter.NE("code", "001"), keyword.Condition{Term: keyword.String("code", "001"), Negated: true}},
		{filter.IsNull("code"), keyword.Condition{Term: keyword.Presence("code"), Negated: true}},
	} {
		got, ok, err := keyword.Lower(test.comparison)
		if err != nil || !ok || got != test.want {
			t.Fatalf("%v: %+v %v %v", test.comparison, got, ok, err)
		}
	}
	for _, comparison := range []*filter.BinaryExpr{filter.EQ("code", 1), filter.GT("code", 1), filter.EQ(filter.Index("tags", 0), "a")} {
		if got, ok, err := keyword.Lower(comparison); ok || err != nil {
			t.Fatalf("%v lowered to %+v %v", comparison, got, err)
		}
	}
}
