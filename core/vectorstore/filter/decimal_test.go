package filter

import (
	"encoding/json"
	"math"
	"testing"
)

func TestNumericComparisonPreservesDecimalMagnitude(t *testing.T) {
	for _, test := range []struct {
		left, right string
		order       int
	}{
		{"1e1000001", "1", 1},
		{"1e-1000001", "0", 1},
		{"-1e1000001", "-1", -1},
		{"-1e-1000001", "0", -1},
		{"0e1000001", "-0e-1000001", 0},
		{"1e9223372036854775808", "1e9223372036854775807", 1},
		{"1e-9223372036854775809", "1e-9223372036854775808", -1},
		{"12e999999999999999999999", "1.2e1000000000000000000000", 0},
		{"0.00012e9223372036854775811", "1.2e9223372036854775807", 0},
		{"120e-9223372036854775809", "1.2e-9223372036854775807", 0},
		{"1E+00000000000000000000001", "10", 0},
		{"-0e999999999999999999999999", "0", 0},
		{"1000", "1e3", 0},
		{"0.00120", "12e-4", 0},
		{"-12.00", "-1.2e1", 0},
		{"1.0001", "1.001", -1},
		{"1.2", "1.2001", -1},
		{"9.99", "10", -1},
		{"-9.99", "-10", 1},
		{"18446744073709551615", "18446744073709551614", 1},
		{"0.10000000000000000001", "0.1", 1},
	} {
		t.Run(test.left+"_versus_"+test.right, func(t *testing.T) {
			order, numeric, ordered := compareNumbers(json.Number(test.left), json.Number(test.right))
			if !numeric || !ordered || order != test.order {
				t.Fatalf("compare(%s, %s) = (%d, %t, %t), want (%d, true, true)", test.left, test.right, order, numeric, ordered, test.order)
			}
			reverse, _, _ := compareNumbers(json.Number(test.right), json.Number(test.left))
			if reverse != -test.order {
				t.Fatalf("reverse comparison = %d, want %d", reverse, -test.order)
			}
		})
	}
}

func TestNumericComparisonRetainsNumericClassification(t *testing.T) {
	for _, value := range []any{math.NaN(), math.Inf(1), math.Inf(-1), json.Number("invalid"), json.Number("null"), json.Number("\"1\""), json.Number("1e+"), json.Number("01"), json.Number("1/2"), json.Number("0x1")} {
		if _, numeric, ordered := compareNumbers(value, 1); !numeric || ordered {
			t.Errorf("compare(%v, 1): numeric=%t ordered=%t", value, numeric, ordered)
		}
	}
	if _, numeric, _ := compareNumbers("1", 1); numeric {
		t.Fatal("string was classified as a number")
	}
}

func TestNumericComparisonPreservesJSONFloatWidths(t *testing.T) {
	for _, test := range []struct {
		name        string
		left, right any
		order       int
	}{
		{"float32 decimal", float32(0.1), json.Number("0.1"), 0},
		{"float64 decimal", float64(0.1), json.Number("0.1"), 0},
		{"promoted float32", float64(float32(0.1)), float32(0.1), 1},
		{"minimum float32", float32(math.SmallestNonzeroFloat32), json.Number("1e-45"), 0},
		{"minimum float64", float64(math.SmallestNonzeroFloat64), json.Number("5e-324"), 0},
		{"maximum float32", float32(math.MaxFloat32), json.Number("3.4028235e38"), 0},
		{"maximum float64", float64(math.MaxFloat64), json.Number("1.7976931348623157e308"), 0},
		{"negative zero", math.Copysign(0, -1), int64(0), 0},
		{"integer precision", uint64(9007199254740993), float64(9007199254740992), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			order, numeric, ordered := compareNumbers(test.left, test.right)
			if !numeric || !ordered || order != test.order {
				t.Fatalf("compare(%v, %v) = (%d, %t, %t), want (%d, true, true)", test.left, test.right, order, numeric, ordered, test.order)
			}
		})
	}
}

func TestMatchPreservesLargeExponentNumbers(t *testing.T) {
	positive := mustParse(t, "n > 0")
	negative := mustParse(t, "n < 0")
	for _, number := range []json.Number{"1e1000001", "1e-1000001", "1e999999999999999999999999"} {
		values := map[string]any{"n": number}
		if !mustMatch(t, positive, values) || mustMatch(t, negative, values) {
			t.Errorf("positive JSON number %s was not ordered above zero", number)
		}
	}
}

func BenchmarkMatchLargeExponent(b *testing.B) {
	predicate, err := Parse("n > 0")
	if err != nil {
		b.Fatal(err)
	}
	values := map[string]any{"n": json.Number("1e1000000")}
	b.ReportAllocs()
	for b.Loop() {
		matched, err := Match(predicate, values)
		if err != nil || !matched {
			b.Fatalf("Match = %t, %v", matched, err)
		}
	}
}
