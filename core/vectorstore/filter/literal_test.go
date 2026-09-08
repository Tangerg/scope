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

func TestLiteralKey(t *testing.T) {
	tests := []struct {
		name    string
		literal *filter.Literal
		want    string
		wantErr bool
	}{
		{name: "string", literal: filter.NewLiteral("name"), want: "name"},
		{name: "signed integer", literal: filter.NewLiteral(42), want: "42"},
		{name: "unsigned integer", literal: filter.NewLiteral(uint64(math.MaxInt64)), want: "9223372036854775807"},
		{name: "integral decimal", literal: filter.NewLiteral(4.0), want: "4"},
		{name: "negative", literal: filter.NewLiteral(-1), wantErr: true},
		{name: "fractional", literal: filter.NewLiteral(1.5), wantErr: true},
		{name: "oversized", literal: filter.NewLiteral(uint64(math.MaxUint64)), wantErr: true},
		{name: "bool", literal: filter.NewLiteral(true), wantErr: true},
		{name: "nil", literal: nil, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.literal.Key()
			if (err != nil) != test.wantErr {
				t.Fatalf("Key() error = %v, wantErr %t", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("Key() = %q, want %q", got, test.want)
			}
		})
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

	// Provider filter grammars document decimal numerals, not exponents, and a
	// store pastes NumberText straight into a filter string. The canonical text
	// of these literals is "1e+06" and "9.223372036854776e+18"; emitting that
	// would hand Typesense and Vespa a numeral their documented grammars never
	// promise to read.
	//
	// 2^63 also pins the boundary that made the adapters' own rendering
	// architecture-dependent: they decided integer-ness with
	// float64(int64(val)) == val, and Go leaves an out-of-range float-to-int
	// conversion implementation-defined — arm64 saturates to MaxInt64, whose
	// float64 equals 2^63, so the comparison passed and the filter carried
	// 9223372036854775807 while amd64 carried the right digits.
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
		if _, err := filter.NewLiteral(1.5).Int64(); err == nil {
			t.Fatal("Int64 accepted a fraction")
		}
		if _, err := filter.NewLiteral(uint64(math.MaxUint64)).Int64(); err == nil {
			t.Fatal("Int64 accepted uint64 overflow")
		}
	})

	t.Run("float64 rejects rounded integer", func(t *testing.T) {
		if _, err := filter.NewLiteral(uint64(1<<53 + 1)).Float64(); err == nil {
			t.Fatal("Float64 accepted a rounded integer")
		}
		if actual, err := filter.NewLiteral(uint64(1 << 54)).Float64(); err != nil || actual != 1<<54 {
			t.Fatalf("Float64(exact power of two) = %v, %v", actual, err)
		}
	})

	t.Run("float32 rejects rounded integer", func(t *testing.T) {
		if _, err := filter.NewLiteral(1<<24 + 1).Float32(); err == nil {
			t.Fatal("Float32 accepted a rounded integer")
		}
		if actual, err := filter.NewLiteral(1.5).Float32(); err != nil || actual != 1.5 {
			t.Fatalf("Float32(1.5) = %v, %v", actual, err)
		}
	})

	t.Run("integer classification and native int", func(t *testing.T) {
		integer := filter.NewLiteral(42)
		if exact, err := integer.IsInteger(); err != nil || !exact {
			t.Fatalf("IsInteger(42) = %t, %v", exact, err)
		}
		if actual, err := integer.Int(); err != nil || actual != 42 {
			t.Fatalf("Int(42) = %d, %v", actual, err)
		}
		if exact, err := filter.NewLiteral(1.5).IsInteger(); err != nil || exact {
			t.Fatalf("IsInteger(1.5) = %t, %v", exact, err)
		}
		if _, err := filter.NewLiteral(uint64(math.MaxUint64)).Int(); err == nil {
			t.Fatal("Int accepted an overflowing uint64")
		}
	})
}
