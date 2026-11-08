package filter_test

import (
	"math"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestDispatchRejectsUnsupportedOperators(t *testing.T) {
	nullTest := mustParseBinary(t, `field is null`)
	if err := nullTest.Dispatch(filter.BinaryHandlers{}); err == nil {
		t.Fatal("Dispatch accepted a null operator")
	}
	if err := (*filter.UnaryExpr)(nil).Dispatch(nil); err == nil {
		t.Fatal("Dispatch accepted a nil unary expression")
	}
}

func TestLiteralValue(t *testing.T) {
	tests := []struct {
		name    string
		literal *filter.Literal
		wantErr bool
	}{
		{name: "string", literal: filter.NewLiteral("scope")},
		{name: "negative integer", literal: filter.NewLiteral(-1)},
		{name: "decimal", literal: filter.NewLiteral(1.5)},
		{name: "bool", literal: filter.NewLiteral(true)},
		{name: "nil", literal: nil, wantErr: true},
		{name: "non-finite", literal: filter.NewLiteral(math.Inf(1)), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.literal.Value()
			if (err != nil) != test.wantErr {
				t.Fatalf("Value() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestListValuesRejectInvalidLists(t *testing.T) {
	tests := []struct {
		name string
		list *filter.ListLiteral
	}{
		{name: "nil", list: nil},
		{name: "empty", list: filter.NewListLiteral([]int{})},
		{name: "nil first", list: filter.NewListLiteral([]*filter.Literal{nil})},
		{name: "mixed kinds", list: filter.NewListLiteral([]*filter.Literal{filter.NewLiteral("a"), filter.NewLiteral(1)})},
		{name: "decimal precision loss", list: filter.NewListLiteral([]*filter.Literal{filter.NewLiteral(1.5), filter.NewLiteral(int64(1<<53 + 1))})},
		{name: "signed unsigned span", list: filter.NewListLiteral([]*filter.Literal{filter.NewLiteral(uint64(math.MaxUint64)), filter.NewLiteral(-1)})},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.list.Values(); err == nil {
				t.Fatal("Values returned nil error")
			}
		})
	}
}

func TestListDecimalValuesPreserveExactIntegers(t *testing.T) {
	tests := []struct {
		input string
		want  []float64
	}{
		{input: "n in (18014398509481984, 0.5)", want: []float64{1 << 54, 0.5}},
		{input: "n in (-18014398509481984, 0.5)", want: []float64{-(1 << 54), 0.5}},
		{input: "n in (9223372036854775808, 0.5)", want: []float64{1 << 63, 0.5}},
		{input: "n in (0.5, 18014398509481984)", want: []float64{0.5, 1 << 54}},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			predicate := mustParseBinary(t, test.input)
			list := predicate.Right().(*filter.ListLiteral)
			values, err := list.Values()
			if err != nil {
				t.Fatal(err)
			}
			actual, ok := values.([]float64)
			if !ok || !slices.Equal(actual, test.want) {
				t.Fatalf("Values() = %v, want %v", values, test.want)
			}
		})
	}
}

func TestExactNumberConversions(t *testing.T) {
	t.Run("exact text", func(t *testing.T) {
		literal := filter.NewLiteral(uint64(math.MaxUint64))
		actual, err := literal.NumberText()
		if err != nil || actual != "18446744073709551615" {
			t.Fatalf("NumberText() = %q, %v", actual, err)
		}
	})

	t.Run("decimal numeral without exponent", func(t *testing.T) {
		tests := []struct {
			name    string
			literal *filter.Literal
			want    string
		}{
			{name: "integral float", literal: filter.NewLiteral(1000000.0), want: "1000000"},
			{name: "fraction", literal: filter.NewLiteral(0.8), want: "0.8"},
			{name: "int64 boundary", literal: filter.NewLiteral(float64(1 << 63)), want: "9223372036854776000"},
			{name: "negative zero", literal: filter.NewLiteral(math.Copysign(0, -1)), want: "0"},
			{name: "plain integer", literal: filter.NewLiteral(2020), want: "2020"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				actual, err := test.literal.NumberText()
				if err != nil || actual != test.want {
					t.Fatalf("NumberText() = %q, %v, want %q", actual, err, test.want)
				}
			})
		}
	})

	t.Run("rejects a non-number literal", func(t *testing.T) {
		if _, err := filter.NewLiteral("2020").NumberText(); err == nil {
			t.Fatal("NumberText accepted a string literal")
		}
	})

	t.Run("int64 rejects fraction and overflow", func(t *testing.T) {
		if _, err := filter.NewLiteral(1.5).AsInt64(); err == nil {
			t.Fatal("Int64 accepted a fraction")
		}
		if _, err := filter.NewLiteral(uint64(math.MaxUint64)).AsInt64(); err == nil {
			t.Fatal("Int64 accepted uint64 overflow")
		}
	})

	t.Run("float64 rejects rounded integer", func(t *testing.T) {
		if _, err := filter.NewLiteral(uint64(1<<53 + 1)).AsFloat64(); err == nil {
			t.Fatal("Float64 accepted a rounded integer")
		}
		if actual, err := filter.NewLiteral(uint64(1 << 54)).AsFloat64(); err != nil || actual != 1<<54 {
			t.Fatalf("Float64(exact power of two) = %v, %v", actual, err)
		}
	})

	t.Run("float32 rejects rounded integer", func(t *testing.T) {
		if _, err := filter.NewLiteral(1<<24 + 1).AsFloat32(); err == nil {
			t.Fatal("Float32 accepted a rounded integer")
		}
		if actual, err := filter.NewLiteral(1.5).AsFloat32(); err != nil || actual != 1.5 {
			t.Fatalf("Float32(1.5) = %v, %v", actual, err)
		}
	})

	t.Run("integer classification and native int", func(t *testing.T) {
		integer := filter.NewLiteral(42)
		if exact, err := integer.IsInteger(); err != nil || !exact {
			t.Fatalf("IsInteger(42) = %t, %v", exact, err)
		}
		if actual, err := integer.AsInt(); err != nil || actual != 42 {
			t.Fatalf("Int(42) = %d, %v", actual, err)
		}
		if exact, err := filter.NewLiteral(1.5).IsInteger(); err != nil || exact {
			t.Fatalf("IsInteger(1.5) = %t, %v", exact, err)
		}
		if _, err := filter.NewLiteral(uint64(math.MaxUint64)).AsInt(); err == nil {
			t.Fatal("Int accepted an overflowing uint64")
		}
	})
}

func TestFloatingLiteralConversionsPreserveSelectedValue(t *testing.T) {
	for _, value := range []float64{1 << 63, -(1 << 63), 1e23, -1e23, math.MaxFloat64} {
		literal := filter.NewLiteral(value)
		if actual, err := literal.AsFloat64(); err != nil || actual != value {
			t.Fatalf("AsFloat64(%g) = %g, %v", value, actual, err)
		}
		list := filter.NewListLiteral([]float64{value, 0.5})
		values, err := list.Values()
		if err != nil {
			t.Fatalf("Values(%g) error = %v", value, err)
		}
		actual, ok := values.([]float64)
		if !ok || !slices.Equal(actual, []float64{value, 0.5}) {
			t.Fatalf("Values(%g) = %v", value, values)
		}
	}
	for _, value := range []float64{1 << 63, -(1 << 63), 0.1} {
		actual, err := filter.NewLiteral(value).AsFloat32()
		if err != nil || actual != float32(value) {
			t.Fatalf("AsFloat32(%g) = %g, %v", value, actual, err)
		}
	}
	for _, value := range []float64{1<<24 + 1, 1e100} {
		if actual, err := filter.NewLiteral(value).AsFloat32(); err == nil {
			t.Fatalf("AsFloat32(%g) = %g, want precision or overflow error", value, actual)
		}
	}
}
