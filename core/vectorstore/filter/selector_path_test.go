package filter_test

import (
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestSelectorPathRejectsInvalidSelectors(t *testing.T) {
	tests := []struct {
		name     string
		selector filter.Selector
	}{
		{"nil identifier", (*filter.Ident)(nil)},
		{"empty identifier", filter.NewIdent("")},
		{"keyword", filter.NewIdent("AND")},
		{"invalid identifier", filter.NewIdent("a b")},
		{"nil index", (*filter.IndexExpr)(nil)},
		{"zero index", &filter.IndexExpr{}},
		{"invalid base", filter.Index("a b", "key")},
		{"nil base", filter.Index((*filter.Ident)(nil), "key")},
		{"nil key", filter.Index("field", (*filter.Literal)(nil))},
		{"negative index", filter.Index("field", -1)},
		{"fractional index", filter.Index("field", 1.5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if value := recover(); value != nil {
					t.Fatalf("Path panicked: %v", value)
				}
			}()
			if path, err := tt.selector.Path(); err == nil || path != nil {
				t.Fatalf("Path() = %v, %v; want nil path and error", path, err)
			}
		})
	}
}

func TestSelectorPathOwnsItsSegments(t *testing.T) {
	selector := filter.Index(filter.Index("profile", "姓名"), 2)
	first, err := selector.Path()
	if err != nil {
		t.Fatal(err)
	}
	first[0] = "changed"
	second, err := selector.Path()
	if err != nil || !slices.Equal(second, []string{"profile", "姓名", "2"}) {
		t.Fatalf("Path() = %v, %v", second, err)
	}
}
