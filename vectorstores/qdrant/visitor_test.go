package qdrant

import (
	"testing"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestVisitor_Conformance(t *testing.T) {
	storetest.VisitorConformance(t,
		func(src string) error {
			expr, err := filter.Parse(src)
			if err != nil {
				return err
			}
			v := newVisitor()
			return expr.Accept(v)
		},
		storetest.Options{
			// Qdrant keyword matching can represent LIKE only when the
			// pattern contains no SQL wildcards.
			Unsupported: []string{"like"},
		},
	)
}

func toFilter(t *testing.T, src string) *qdrantclient.Filter {
	t.Helper()
	expr, err := filter.Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	v := newVisitor()
	if err := expr.Accept(v); err != nil {
		t.Fatalf("visit %q: %v", src, err)
	}
	return v.snapshot()
}

func isEmptyKey(cond *qdrantclient.Condition) string {
	in, ok := cond.GetConditionOneOf().(*qdrantclient.Condition_IsEmpty)
	if !ok || in.IsEmpty == nil {
		return ""
	}
	return in.IsEmpty.GetKey()
}

func TestVisitor_IsNull(t *testing.T) {
	f := toFilter(t, "author is null")

	if len(f.GetMust()) != 1 {
		t.Fatalf("expected 1 Must condition, got %d", len(f.GetMust()))
	}
	if len(f.GetMustNot()) != 0 {
		t.Fatalf("expected 0 MustNot conditions, got %d", len(f.GetMustNot()))
	}
	if key := isEmptyKey(f.GetMust()[0]); key != "author" {
		t.Fatalf("expected IsEmpty condition on key %q, got %q", "author", key)
	}
}

func TestVisitor_IsNotNull(t *testing.T) {
	f := toFilter(t, "author is not null")

	// IS NOT NULL = NOT(author IS NULL): the IsEmpty condition is wrapped in
	// a nested filter under MustNot.
	if len(f.GetMustNot()) != 1 {
		t.Fatalf("expected 1 MustNot condition, got %d", len(f.GetMustNot()))
	}
	if len(f.GetMust()) != 0 {
		t.Fatalf("expected 0 Must conditions, got %d", len(f.GetMust()))
	}

	nested, ok := f.GetMustNot()[0].GetConditionOneOf().(*qdrantclient.Condition_Filter)
	if !ok {
		t.Fatalf("expected nested filter condition under MustNot, got %T",
			f.GetMustNot()[0].GetConditionOneOf())
	}
	inner := nested.Filter.GetMust()
	if len(inner) != 1 {
		t.Fatalf("expected 1 condition inside nested filter, got %d", len(inner))
	}
	if key := isEmptyKey(inner[0]); key != "author" {
		t.Fatalf("expected nested IsEmpty condition on key %q, got %q", "author", key)
	}
}

func TestVisitor_CollectionMembershipUsesExactArrayMatch(t *testing.T) {
	result := toFilter(t, `visible_to has 'user-42'`)
	conditions := result.GetMust()
	if len(conditions) != 1 || conditions[0].GetField().GetKey() != "visible_to" ||
		conditions[0].GetField().GetMatch().GetKeyword() != "user-42" {
		t.Fatalf("conditions = %#v", conditions)
	}
}

func TestVisitor_RejectsFractionalMatchValues(t *testing.T) {
	for name, predicate := range map[string]filter.Predicate{
		"equality":   filter.EQ("score", 1.9),
		"membership": filter.In("score", []float64{1, 1.9}),
	} {
		t.Run(name, func(t *testing.T) {
			visitor := newVisitor()
			if err := predicate.Accept(visitor); err == nil {
				t.Fatal("Qdrant silently accepted a fractional integer match")
			}
		})
	}
}

func TestVisitor_LikeOnlyAcceptsExactPatterns(t *testing.T) {
	visitor := newVisitor()
	if err := filter.Like("title", "guide").Accept(visitor); err != nil {
		t.Fatalf("visit exact LIKE pattern: %v", err)
	}
	conditions := visitor.snapshot().GetMust()
	if len(conditions) != 1 || conditions[0].GetField().GetKey() != "title" ||
		conditions[0].GetField().GetMatch().GetKeyword() != "guide" {
		t.Fatalf("exact LIKE condition = %#v, want keyword title=guide", conditions)
	}

	for _, pattern := range []string{"guide%", "%guide", "g_ide"} {
		t.Run(pattern, func(t *testing.T) {
			if err := filter.Like("title", pattern).Accept(newVisitor()); err == nil {
				t.Fatalf("Qdrant silently accepted inexact LIKE pattern %q", pattern)
			}
		})
	}
}
