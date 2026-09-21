package typesense

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestVisitorCollectionMembershipUsesExactArrayMatch(t *testing.T) {
	visitor := newVisitor("metadata")
	if err := filter.Has("visible_to", "user-42").Accept(visitor); err != nil {
		t.Fatal(err)
	}
	if got, want := visitor.snapshot(), "metadata.visible_to:= user-42"; got != want {
		t.Fatalf("Result() = %q, want %q", got, want)
	}
}

func TestNegatedOrderingRejectsUnrepresentableNullBranch(t *testing.T) {
	predicate := filter.Not(filter.GT("price", 5))
	if err := predicate.Accept(newVisitor("metadata")); err == nil {
		t.Fatal("negated ordering silently omitted missing and null records")
	}
}
