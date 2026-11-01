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
