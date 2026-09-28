package filter_test

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestParseRejectsInvalidUTF8(t *testing.T) {
	for _, input := range []string{
		"name == '\xff'",
		"name IN ('ok', '\xc0\xaf')",
		"metadata['\xed\xa0\x80'] == 'ok'",
	} {
		if predicate, err := filter.Parse(input); err == nil || predicate != nil {
			t.Errorf("Parse(%q) = %v, %v; want nil predicate and error", input, predicate, err)
		}
	}
}

func TestPredicatesRejectInvalidUTF8Literals(t *testing.T) {
	invalid := "\xff"
	for name, predicate := range map[string]filter.Predicate{
		"equal":     filter.EQ("field", invalid),
		"not equal": filter.NE("field", invalid),
		"in":        filter.In("field", []string{"valid", invalid}),
		"has":       filter.Has("field", invalid),
		"like":      filter.Like("field", invalid),
		"index":     filter.EQ(filter.Index("field", invalid), "value"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := predicate.Validate(); err == nil {
				t.Fatal("Validate accepted invalid UTF-8")
			}
		})
	}
	for name, decode := range map[string]func() error{
		"string": func() error { _, err := filter.NewLiteral(invalid).AsString(); return err },
		"value":  func() error { _, err := filter.NewLiteral(invalid).Value(); return err },
		"list":   func() error { _, err := filter.NewListLiteral([]string{invalid}).Values(); return err },
		"path":   func() error { _, err := filter.Index("field", invalid).Path(); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := decode(); err == nil {
				t.Fatal("projection accepted invalid UTF-8")
			}
		})
	}
}

func TestReplacementRuneRemainsValidUTF8(t *testing.T) {
	const value = "�"
	parsed, err := filter.Parse("name == '" + value + "'")
	if err != nil {
		t.Fatal(err)
	}
	constructed := filter.EQ("name", value)
	if validationErr := constructed.Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
	if !constructed.Equal(parsed) {
		t.Fatalf("constructed %v differs from parsed %v", constructed, parsed)
	}
	matched, err := filter.Match(parsed, map[string]any{"name": value})
	if err != nil || !matched {
		t.Fatalf("Match() = %v, %v", matched, err)
	}
}
