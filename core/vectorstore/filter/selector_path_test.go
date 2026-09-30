package filter_test

import (
	"math"
	"strconv"
	"strings"
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
		{"oversized index", filter.Index("field", uint64(math.MaxInt64)+1)},
		{"bool index", filter.Index("field", filter.NewLiteral(true))},
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
	first[0] = first[2]
	second, err := selector.Path()
	if err != nil || pathText(second) != "profile/姓名/[2]" {
		t.Fatalf("Path() = %s, %v", pathText(second), err)
	}
}

func TestSelectorPathKeepsSegmentKinds(t *testing.T) {
	tests := []struct {
		name     string
		selector filter.Selector
		want     string
	}{
		{name: "numeric index", selector: filter.Index("items", 0), want: "items/[0]"},
		{name: "digit key", selector: filter.Index("items", "0"), want: "items/0"},
		{name: "integral decimal index", selector: filter.Index("items", 4.0), want: "items/[4]"},
		{name: "largest index", selector: filter.Index("items", uint64(math.MaxInt64)), want: "items/[9223372036854775807]"},
		{name: "index then key", selector: filter.Index(filter.Index("items", 1), "name"), want: "items/[1]/name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := tt.selector.Path()
			if err != nil {
				t.Fatal(err)
			}
			if got := pathText(path); got != tt.want {
				t.Fatalf("Path() = %s, want %s", got, tt.want)
			}
		})
	}

	parsed, err := filter.Parse(`items[0] == 'a' and items['0'] == 'a'`)
	if err != nil {
		t.Fatal(err)
	}
	binary, ok := parsed.(*filter.BinaryExpr)
	if !ok {
		t.Fatalf("Parse() = %T, want *filter.BinaryExpr", parsed)
	}
	index, key := binary.Left().(*filter.BinaryExpr), binary.Right().(*filter.BinaryExpr)
	indexPath, indexErr := index.Path()
	keyPath, keyErr := key.Path()
	if indexErr != nil || keyErr != nil || pathText(indexPath) != "items/[0]" || pathText(keyPath) != "items/0" {
		t.Fatalf("parsed paths = %s, %s (%v, %v), want items/[0] and items/0", pathText(indexPath), pathText(keyPath), indexErr, keyErr)
	}
}

// pathText renders index segments in brackets so a test can tell them apart
// from keys spelled with digits.
func pathText(path []filter.PathSegment) string {
	parts := make([]string, 0, len(path))
	for _, segment := range path {
		if index, ok := segment.Index(); ok {
			parts = append(parts, "["+strconv.FormatUint(index, 10)+"]")
			continue
		}
		key, _ := segment.Key()
		parts = append(parts, key)
	}
	return strings.Join(parts, "/")
}
