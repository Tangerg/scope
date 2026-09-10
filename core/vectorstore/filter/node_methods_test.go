package filter_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// mustParseBinary parses src and asserts the result is a [*filter.BinaryExpr].
func mustParseBinary(t *testing.T, src string) *filter.BinaryExpr {
	t.Helper()
	expr, err := filter.Parse(src)
	if err != nil {
		t.Fatalf("filter.Parse(%q): %v", src, err)
	}
	be, ok := expr.(*filter.BinaryExpr)
	if !ok {
		t.Fatalf("expected *filter.BinaryExpr, got %T", expr)
	}
	return be
}

func TestBinaryExprDispatchRoutes(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"and is logical", `a == 1 and b == 2`, "logical"},
		{"or is logical", `a == 1 or b == 2`, "logical"},
		{"eq is comparison", `a == 1`, "comparison"},
		{"ne is comparison", `a != 1`, "comparison"},
		{"lt is comparison", `a < 1`, "comparison"},
		{"gte is comparison", `a >= 1`, "comparison"},
		{"in is membership", `a in (1, 2)`, "in"},
		{"has is collection membership", `a has 1`, "has"},
		{"like is pattern", `a like '%foo%'`, "like"},
		{"is is null test", `a is null`, "null"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := mustParseBinary(t, tc.src)
			var got string
			err := e.Dispatch(filter.BinaryHandlers{
				Logical:    func(*filter.BinaryExpr) error { got = "logical"; return nil },
				Comparison: func(*filter.BinaryExpr) error { got = "comparison"; return nil },
				In:         func(*filter.BinaryExpr) error { got = "in"; return nil },
				Has:        func(*filter.BinaryExpr) error { got = "has"; return nil },
				Like:       func(*filter.BinaryExpr) error { got = "like"; return nil },
				NullTest:   func(*filter.BinaryExpr) error { got = "null"; return nil },
			})
			if err != nil {
				t.Fatalf("BinaryExpr.Dispatch: %v", err)
			}
			if got != tc.want {
				t.Fatalf("routed to %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBinaryExprDispatchMissingHandlerIsExplicitlyUnsupported(t *testing.T) {
	expr := mustParseBinary(t, `tags has 'rag'`)
	err := expr.Dispatch(filter.BinaryHandlers{})
	if err == nil || !strings.Contains(err.Error(), "HAS is not supported") {
		t.Fatalf("error = %v, want explicit HAS unsupported error", err)
	}
}

func TestBinaryExprDispatchHandlerErrorPropagates(t *testing.T) {
	want := errors.New("boom")
	e := mustParseBinary(t, `a == 1`)
	err := e.Dispatch(filter.BinaryHandlers{
		Comparison: func(*filter.BinaryExpr) error { return want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want chain to %v", err, want)
	}
}

func TestUnaryExprDispatchNot(t *testing.T) {
	expr, err := filter.Parse(`not (a == 1)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u, ok := expr.(*filter.UnaryExpr)
	if !ok {
		t.Fatalf("expected *filter.UnaryExpr, got %T", expr)
	}
	var got string
	err = u.Dispatch(
		func(*filter.UnaryExpr) error { got = "not"; return nil },
	)
	if err != nil {
		t.Fatalf("UnaryExpr.Dispatch: %v", err)
	}
	if got != "not" {
		t.Fatalf("got %q, want not", got)
	}
}

func TestOperatorLogicalString(t *testing.T) {
	if op, _ := filter.OpAnd.LogicalString(); op != "AND" {
		t.Fatalf("AND → %q, want AND", op)
	}
	if op, _ := filter.OpOr.LogicalString(); op != "OR" {
		t.Fatalf("OR → %q, want OR", op)
	}
	if _, err := filter.OpEqual.LogicalString(); err == nil {
		t.Fatal("non-logical kind must error")
	}
}

func TestBinaryExprListNonEmpty(t *testing.T) {
	e := mustParseBinary(t, `a in (1, 2, 3)`)
	list, err := e.List()
	if err != nil {
		t.Fatalf("BinaryExpr.List: %v", err)
	}
	if got := list.Len(); got != 3 {
		t.Fatalf("len = %d, want 3", got)
	}
}

func TestBinaryExprListRejectsNonList(t *testing.T) {
	e := mustParseBinary(t, `a == 1`)
	_, err := e.List()
	if err == nil || !strings.Contains(err.Error(), "list") {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestBinaryExprPattern(t *testing.T) {
	e := mustParseBinary(t, `a like '%foo%'`)
	got, err := e.Pattern()
	if err != nil {
		t.Fatalf("BinaryExpr.Pattern: %v", err)
	}
	if got != "%foo%" {
		t.Fatalf("got %q, want %%foo%%", got)
	}

	bad := filter.Like("a", filter.NewLiteral(42))
	if _, err := bad.Pattern(); err == nil {
		t.Fatal("non-string pattern must error")
	}
}

func TestListLiteralValuesStrings(t *testing.T) {
	e := mustParseBinary(t, `a in ('x', 'y', 'z')`)
	list, _ := e.List()

	slice, err := list.Values()
	if err != nil {
		t.Fatalf("ListLiteral.Values: %v", err)
	}
	xs, ok := slice.([]string)
	if !ok {
		t.Fatalf("slice type = %T, want []string", slice)
	}
	if len(xs) != 3 || xs[0] != "x" || xs[2] != "z" {
		t.Fatalf("xs = %v, want [x y z]", xs)
	}
}

func TestListLiteralValuesNumbers(t *testing.T) {
	e := mustParseBinary(t, `a in (1, 2, 3.5)`)
	list, _ := e.List()

	slice, err := list.Values()
	if err != nil {
		t.Fatalf("ListLiteral.Values: %v", err)
	}
	ns, ok := slice.([]float64)
	if !ok {
		t.Fatalf("slice type = %T, want []float64", slice)
	}
	if len(ns) != 3 || ns[2] != 3.5 {
		t.Fatalf("ns = %v, want [1 2 3.5]", ns)
	}
}

func TestListLiteralValuesIntegersStayExact(t *testing.T) {
	e := mustParseBinary(t, `a in (1, 2, 3)`)
	list, _ := e.List()

	slice, err := list.Values()
	if err != nil {
		t.Fatal(err)
	}
	values, ok := slice.([]int64)
	if !ok || len(values) != 3 || values[2] != 3 {
		t.Fatalf("slice = %#v (%T), want []int64{1, 2, 3}", slice, slice)
	}
}

func TestLiteralValuePreservesUint64(t *testing.T) {
	literal := filter.NewLiteral(uint64(math.MaxUint64))
	value, err := literal.Value()
	if err != nil {
		t.Fatal(err)
	}
	if value != uint64(math.MaxUint64) {
		t.Fatalf("value = %#v (%T), want MaxUint64", value, value)
	}
}

func TestSelectorPathIncludesBaseIdentifier(t *testing.T) {
	tests := []struct {
		name string
		expr filter.Selector
		want []string
	}{
		{name: "bare", expr: filter.NewIdent("author"), want: []string{"author"}},
		{name: "nested", expr: filter.Index(filter.Index("profile", "name"), "first"), want: []string{"profile", "name", "first"}},
		{name: "numeric", expr: filter.Index("items", 2), want: []string{"items", "2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.expr.Path()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "/") != strings.Join(tt.want, "/") {
				t.Fatalf("path = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBinaryOperandsExposeSemanticValues(t *testing.T) {
	expr := filter.EQ(filter.Index("profile", "name"), "scope")
	selector, err := expr.Selector()
	if err != nil {
		t.Fatal(err)
	}
	indexed, ok := selector.(*filter.IndexExpr)
	if !ok || indexed.Left() == nil || indexed.Index() == nil {
		t.Fatalf("selector = %#v, want populated IndexExpr", selector)
	}
	path, err := expr.Path()
	if err != nil || strings.Join(path, "/") != "profile/name" {
		t.Fatalf("Path() = %v, %v", path, err)
	}
	value, err := expr.Value()
	if err != nil || value != "scope" {
		t.Fatalf("Value() = %#v, %v", value, err)
	}

	if _, err := (*filter.BinaryExpr)(nil).Selector(); err == nil {
		t.Fatal("nil BinaryExpr.Selector returned nil error")
	}
	if _, err := filter.And(filter.EQ("a", 1), filter.EQ("b", 2)).Selector(); err == nil {
		t.Fatal("logical BinaryExpr.Selector returned nil error")
	}
}

func TestListLiteralValuesBools(t *testing.T) {
	e := mustParseBinary(t, `a in (true, false, true)`)
	list, _ := e.List()

	slice, err := list.Values()
	if err != nil {
		t.Fatalf("ListLiteral.Values: %v", err)
	}
	bs, ok := slice.([]bool)
	if !ok {
		t.Fatalf("slice type = %T, want []bool", slice)
	}
	if len(bs) != 3 || bs[0] != true || bs[1] != false {
		t.Fatalf("bs = %v, want [true false true]", bs)
	}
}
