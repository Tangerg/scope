package redis

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// TestVisitor_Conformance runs the shared visitor suite against the
// redis (RediSearch) visitor. Redis is schema-required, so the
// per-field-type declarations below mirror the [storetest] case
// identifiers; redis IN-on-numeric isn't supported by the visitor
// so `in_numbers` is declared via [storetest.Options.Unsupported].
func TestVisitor_Conformance(t *testing.T) {
	fields := map[string]MetadataFieldType{
		"author":         FieldTag, // == / !=
		"year":           FieldNumeric,
		"published":      FieldTag, // bool ==
		"n":              FieldNumeric,
		"a":              FieldNumeric,
		"b":              FieldNumeric,
		"c":              FieldNumeric,
		"d":              FieldNumeric,
		"tags":           FieldTag,  // IN strings
		"flags":          FieldTag,  // IN bools (rendered as tag string)
		"title":          FieldText, // LIKE
		"profile.author": FieldTag,
		"profile.a.b":    FieldTag,
	}

	storetest.VisitorConformance(t,
		func(src string) error {
			expr, err := filter.Parse(src)
			if err != nil {
				return err
			}
			v := newVisitor(fields)
			return expr.Accept(v)
		},
		storetest.Options{
			// Redis doesn't support IN on NUMERIC fields — the visitor
			// errors with "IN is not supported on field type". This is
			// a real capability gap, not a visitor bug.
			Unsupported: []string{"in_numbers", "collection_membership", "null_test", "not_null_test"},
		},
	)
}

func TestVisitor_RejectsIntegerThatRediSearchCannotRepresentExactly(t *testing.T) {
	visitor := newVisitor(map[string]MetadataFieldType{"id": FieldNumeric})
	if err := filter.EQ("id", uint64(1<<53+1)).Accept(visitor); err == nil {
		t.Fatal("Redis silently rounded a large integer")
	}
}

func TestVisitor_TranslatesLikeToRedisWildcardQuery(t *testing.T) {
	t.Parallel()

	visitor := newVisitor(map[string]MetadataFieldType{"title": FieldText})
	if err := filter.Like("title", `intro%_literal*?`).Accept(visitor); err != nil {
		t.Fatal(err)
	}
	if got, want := visitor.snapshot(), `@title:(w'intro*?literal\*\?')`; got != want {
		t.Fatalf("Result() = %q, want %q", got, want)
	}
}
