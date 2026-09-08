package weaviate

import (
	"strings"
	"testing"

	"github.com/weaviate/weaviate-go-client/v5/weaviate/filters"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// testProperties declares every key the shared suite and the tests below
// select on. A Weaviate where filter may only name a declared property, so a
// compiler test has to say which keys the class carries.
func testProperties() []MetadataProperty {
	names := []string{
		"author", "year", "published", "n", "a", "b", "c", "d",
		"tags", "years", "flags", "title", "visible_to", "profile", "score",
		"count", "status",
	}
	properties := make([]MetadataProperty, 0, len(names))
	for _, name := range names {
		properties = append(properties, MetadataProperty{Name: name, DataType: MetadataDataText})
	}
	return properties
}

func compileTestFilter(predicate filter.Predicate) (*filters.WhereBuilder, error) {
	visitor := newVisitor(testProperties())
	if err := predicate.Accept(visitor); err != nil {
		return nil, err
	}
	return visitor.snapshot(), nil
}

func TestVisitor_Conformance(t *testing.T) {
	storetest.VisitorConformance(t, func(src string) error {
		expr, err := filter.Parse(src)
		if err != nil {
			return err
		}
		v := newVisitor(testProperties())
		return expr.Accept(v)
	},
		storetest.Options{
			// A nested metadata key would need an object property with
			// declared nestedProperties, and dotted-path filtering on those
			// leaves is a Weaviate v1.38 preview feature.
			Unsupported: []string{"indexed_key", "nested_index"},
			// This compiler writes a metadata key into the query language as text,
			// so a key that language cannot name is refused rather than approximated.
			InterpolatesKeyPaths: true,
		},
	)
}

// A where filter may only name a declared property, so a filter on any other
// key is refused rather than compiled into a path with no matching field.
func TestVisitor_RefusesUndeclaredKey(t *testing.T) {
	t.Parallel()

	expr, err := filter.Parse("unlisted == 'x'")
	if err != nil {
		t.Fatal(err)
	}
	_, err = compileTestFilter(expr)
	if err == nil || !strings.Contains(err.Error(), "not declared in MetadataProperties") {
		t.Fatalf("compile = %v, want an undeclared-key error", err)
	}
}

func TestVisitor_IsNull(t *testing.T) {
	expr, err := filter.Parse("author is null")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, err := compileTestFilter(expr)
	if err != nil {
		t.Fatalf("Visit: %v", err)
	}
	if got == nil {
		t.Fatal("expected a filter, got nil")
	}

	wf := got.Build()
	if wf.Operator != string(filters.IsNull) {
		t.Errorf("operator = %q, want %q", wf.Operator, filters.IsNull)
	}
	if len(wf.Path) != 1 || wf.Path[0] != "author" {
		t.Errorf("path = %v, want [author]", wf.Path)
	}
	if wf.ValueBoolean == nil || *wf.ValueBoolean != true {
		t.Errorf("valueBoolean = %v, want true", wf.ValueBoolean)
	}
}

func TestVisitor_IsNotNull(t *testing.T) {
	expr, err := filter.Parse("author is not null")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, err := compileTestFilter(expr)
	if err != nil {
		t.Fatalf("Visit: %v", err)
	}
	if got == nil {
		t.Fatal("expected a filter, got nil")
	}

	// IS NOT NULL is rendered as NOT(author IS NULL): an outer Not operator
	// wrapping the IsNull operand produced by the IS NULL handler.
	wf := got.Build()
	if wf.Operator != string(filters.Not) {
		t.Fatalf("outer operator = %q, want %q", wf.Operator, filters.Not)
	}
	if len(wf.Operands) != 1 {
		t.Fatalf("operands = %d, want 1", len(wf.Operands))
	}

	inner := wf.Operands[0]
	if inner.Operator != string(filters.IsNull) {
		t.Errorf("inner operator = %q, want %q", inner.Operator, filters.IsNull)
	}
	if len(inner.Path) != 1 || inner.Path[0] != "author" {
		t.Errorf("inner path = %v, want [author]", inner.Path)
	}
	if inner.ValueBoolean == nil || *inner.ValueBoolean != true {
		t.Errorf("inner valueBoolean = %v, want true", inner.ValueBoolean)
	}
}

func TestVisitor_RejectsIntegerThatNumberFilterCannotRepresent(t *testing.T) {
	_, err := compileTestFilter(filter.EQ("id", uint64(1)<<63))
	if err == nil {
		t.Fatal("Weaviate accepted an integer larger than its int64 data type")
	}
}

func TestVisitor_PreservesIntegerAndNumberTypes(t *testing.T) {
	integer, err := compileTestFilter(filter.GT("count", 42))
	if err != nil {
		t.Fatalf("integer filter: %v", err)
	}
	if got := integer.Build().ValueInt; got == nil || *got != 42 {
		t.Fatalf("ValueInt = %v, want 42", got)
	}

	number, err := compileTestFilter(filter.GT("score", 4.2))
	if err != nil {
		t.Fatalf("number filter: %v", err)
	}
	if got := number.Build().ValueNumber; got == nil || *got != 4.2 {
		t.Fatalf("ValueNumber = %v, want 4.2", got)
	}
}

func TestVisitor_RejectsMixedMembershipTypes(t *testing.T) {
	if _, err := filter.Parse(`status in ('active', 1)`); err == nil {
		t.Fatal("filter parser accepted an IN list with mixed element types")
	}
}

func TestVisitor_DistinguishesInFromCollectionMembership(t *testing.T) {
	inFilter, err := compileTestFilter(filter.In("status", []string{"active", "pending"}))
	if err != nil {
		t.Fatal(err)
	}
	in := inFilter.Build()
	if in.Operator != string(filters.Or) || len(in.Operands) != 2 {
		t.Fatalf("IN filter = %#v, want OR of two equality filters", in)
	}
	for _, operand := range in.Operands {
		if operand.Operator != string(filters.Equal) {
			t.Fatalf("IN operand = %#v, want Equal", operand)
		}
	}

	hasFilter, err := compileTestFilter(filter.Has("visible_to", "user-42"))
	if err != nil {
		t.Fatal(err)
	}
	has := hasFilter.Build()
	if has.Operator != string(filters.ContainsAny) || len(has.Path) != 1 || has.Path[0] != "visible_to" ||
		len(has.ValueTextArray) != 1 || has.ValueTextArray[0] != "user-42" {
		t.Fatalf("HAS filter = %#v, want ContainsAny visible_to=user-42", has)
	}
}

func TestVisitor_TranslatesSQLLikeWildcards(t *testing.T) {
	expr, err := filter.Parse(`title like 'intro_%'`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := compileTestFilter(expr)
	if err != nil {
		t.Fatalf("Visit: %v", err)
	}
	if value := got.Build().ValueText; value == nil || *value != "intro?*" {
		t.Fatalf("ValueText = %v, want intro?*", value)
	}
}

func TestVisitor_RejectsUnrepresentableLikeLiteral(t *testing.T) {
	expr, err := filter.Parse(`title like 'literal*'`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = compileTestFilter(expr)
	if err == nil || !strings.Contains(err.Error(), "cannot represent") {
		t.Fatalf("error = %v, want unrepresentable-pattern error", err)
	}
}
