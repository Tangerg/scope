package bedrockkb

import (
	"math"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func compileFilter(predicate filter.Predicate) (types.RetrievalFilter, error) {
	visitor := newVisitor()
	if err := predicate.Accept(visitor); err != nil {
		return nil, err
	}
	return visitor.snapshot(), nil
}

func TestVisitor_Conformance(t *testing.T) {
	storetest.VisitorConformance(t,
		func(src string) error {
			predicate, err := filter.Parse(src)
			if err != nil {
				return err
			}
			return predicate.Accept(newVisitor())
		},
		storetest.Options{
			Unsupported: []string{"indexed_key", "nested_index", "null_test", "not_null_test"},
			// This compiler writes a metadata key into the query language as text,
			// so a key that language cannot name is refused rather than approximated.
			InterpolatesKeyPaths: true,
		},
	)
}

func TestVisitor_PreservesLargeInteger(t *testing.T) {
	result, err := compileFilter(filter.EQ("id", uint64(math.MaxUint64)))
	if err != nil {
		t.Fatal(err)
	}
	equals, ok := result.(*types.RetrievalFilterMemberEquals)
	if !ok {
		t.Fatalf("filter = %T", result)
	}
	encoded, err := equals.Value.Value.MarshalSmithyDocument()
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "18446744073709551615" {
		t.Fatalf("encoded value = %s", encoded)
	}
}

func TestVisitor_LikePreservesSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		want    any
	}{
		{name: "exact", pattern: "alpha", want: (*types.RetrievalFilterMemberEquals)(nil)},
		{name: "prefix", pattern: "alpha%", want: (*types.RetrievalFilterMemberStartsWith)(nil)},
		{name: "contains", pattern: "%alpha%", want: (*types.RetrievalFilterMemberStringContains)(nil)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := compileFilter(filter.Like("name", test.pattern))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := reflect.TypeOf(result), reflect.TypeOf(test.want); got != want {
				t.Fatalf("filter type = %v, want %v", got, want)
			}
		})
	}
}

func TestVisitor_RejectsLossyLikePatterns(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{"%suffix", "a_b", "a%b", "%"} {
		if _, err := compileFilter(filter.Like("name", pattern)); err == nil {
			t.Errorf("Visit(LIKE %q) error = nil, want unsupported pattern", pattern)
		}
	}
}

func TestVisitor_NotIn(t *testing.T) {
	t.Parallel()

	result, err := compileFilter(filter.Not(filter.In("status", []string{"draft", "deleted"})))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(*types.RetrievalFilterMemberNotIn); !ok {
		t.Fatalf("filter = %T, want *types.RetrievalFilterMemberNotIn", result)
	}
}

func TestVisitor_CollectionMembership(t *testing.T) {
	t.Parallel()

	result, err := compileFilter(filter.Has("visible_to", "user-42"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(*types.RetrievalFilterMemberListContains); !ok {
		t.Fatalf("filter = %T, want *types.RetrievalFilterMemberListContains", result)
	}
}
