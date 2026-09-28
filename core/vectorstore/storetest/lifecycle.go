package storetest

import (
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Snapshot must return a value suitable for reflect.DeepEqual.
type Compiler struct {
	Visit    func(filter.Predicate) error
	Snapshot func() any
}

// VisitorLifecycle verifies that a compiler resets before every visit and
// remains reusable after rejecting a malformed predicate.
func VisitorLifecycle(t *testing.T, factory func() Compiler) {
	t.Helper()
	if factory == nil {
		t.Fatal("storetest.VisitorLifecycle: factory is nil")
	}

	want := factory()
	validateCompiler(t, want)
	last := filter.EQ("b", 2)
	if err := want.Visit(last); err != nil {
		t.Fatalf("fresh Visit: %v", err)
	}

	got := factory()
	validateCompiler(t, got)
	if err := got.Visit(filter.EQ("a", 1)); err != nil {
		t.Fatalf("first Visit: %v", err)
	}
	if err := got.Visit(last); err != nil {
		t.Fatalf("reused Visit: %v", err)
	}
	assertSnapshot(t, got.Snapshot(), want.Snapshot())

	if err := got.Visit(nil); err == nil {
		t.Fatal("nil predicate must fail")
	}
	if err := got.Visit(last); err != nil {
		t.Fatalf("Visit after failure: %v", err)
	}
	assertSnapshot(t, got.Snapshot(), want.Snapshot())
}

func validateCompiler(t *testing.T, compiler Compiler) {
	t.Helper()
	if compiler.Visit == nil || compiler.Snapshot == nil {
		t.Fatal("storetest.VisitorLifecycle: factory returned an incomplete compiler")
	}
}

func assertSnapshot(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result after reuse = %#v, want fresh result %#v", got, want)
	}
}
