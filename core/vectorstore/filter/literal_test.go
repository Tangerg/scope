package filter_test

import (
	"math"
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
		{name: "decimal precision loss", list: filter.NewListLiteral([]*filter.Literal{filter.NewLiteral(1.5), filter.NewLiteral(int64(1 << 54))})},
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
