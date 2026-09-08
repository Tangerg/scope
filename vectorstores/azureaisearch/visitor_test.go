package azureaisearch

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Azure's $filter has no string function to build a pattern match on, and
// search.ismatch runs an analyzed full-text query instead of matching the whole
// value, so LIKE is refused rather than approximated.
func TestLikeIsRefused(t *testing.T) {
	t.Parallel()

	visitor := newVisitor()
	err := filter.Like("author", "Alice%").Accept(visitor)
	if err == nil {
		t.Fatal("Accept(LIKE) = nil, want an unsupported-operator error")
	}
	if !strings.Contains(err.Error(), "LIKE operator is not supported") {
		t.Fatalf("Accept(LIKE) = %v, want an unsupported-operator error", err)
	}
	if strings.Contains(err.Error(), "ismatch") {
		t.Fatalf("Accept(LIKE) = %v; the error must not suggest a full-text substitute", err)
	}
}

func TestCollectionMembershipUsesODataAny(t *testing.T) {
	t.Parallel()

	visitor := newVisitor()
	if err := filter.Has("visible_to", "user-42").Accept(visitor); err != nil {
		t.Fatal(err)
	}
	if got, want := visitor.snapshot(), `visible_to/any(element: element eq 'user-42')`; got != want {
		t.Fatalf("Result() = %q, want %q", got, want)
	}
}
