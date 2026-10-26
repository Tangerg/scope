package cohere

import (
	"math"
	"strings"
	"testing"
)

func TestNativeCountPreservesIntegralDecimalMeaning(t *testing.T) {
	for _, test := range []struct {
		name, wire string
		want       int64
	}{
		{"zero", "0", 0},
		{"negative zero", "-0", 0},
		{"zero with huge positive exponent", "0.000e1000000000", 0},
		{"zero with unbounded positive exponent", "0e999999999999999999999999999999999999", 0},
		{"negative zero with unbounded negative exponent", "-0.000e-999999999999999999999999999999999999", 0},
		{"integral decimal", "1.00e0", 1},
		{"negative exponent cancellation", "1000e-3", 1},
		{"whitespace", " \t12.0\n ", 12},
		{"above floating precision", "9007199254740993.000", 9007199254740993},
		{"maximum", "9223372036854775807", math.MaxInt64},
		{"maximum decimal exponent", "9.223372036854775807e18", math.MaxInt64},
		{"maximum trailing zero", "92233720368547758070e-1", math.MaxInt64},
		{"long trailing zero cancellation", "123" + strings.Repeat("0", 100000) + "e-100000", 123},
		{"long leading zero cancellation", "0." + strings.Repeat("0", 100000) + "123e100003", 123},
		{"long exponent with leading zeros", "1e+" + strings.Repeat("0", 100000) + "0", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			count := nativeCount(17)
			if err := count.UnmarshalJSON([]byte(test.wire)); err != nil || int64(count) != test.want {
				t.Fatalf("count=%d want=%d error=%v", count, test.want, err)
			}
		})
	}
}

func TestNativeCountRejectsUnrepresentableValuesWithoutChangingReceiver(t *testing.T) {
	for _, test := range []struct{ name, wire string }{
		{"huge positive exponent", "1e1000000000"},
		{"huge negative exponent", "1e-1000000000"},
		{"positive exponent beyond int64", "1e9223372036854775808"},
		{"negative exponent beyond int64", "1e-9223372036854775809"},
		{"maximum exponent", "1e9223372036854775807"},
		{"minimum exponent", "1e-9223372036854775808"},
		{"fraction", "1000e-4"},
		{"fraction below integer", "0.999999999999999999999999999999999999999"},
		{"fraction above integer", "1.0000000000000000000000000000000000000001"},
		{"large nonintegral decimal", "9007199254740992.0000000001"},
		{"overflow", "9223372036854775808"},
		{"overflow decimal exponent", "9.223372036854775808e18"},
		{"negative integer", "-1"},
		{"negative fraction", "-0.1"},
		{"quoted number", `"1"`},
		{"boolean", "true"},
		{"null", "null"},
		{"positive sign", "+1"},
		{"leading zero", "01"},
		{"missing fractional digits", "1."},
		{"missing exponent", "1e"},
		{"missing integer digits", ".0"},
		{"NaN", "NaN"},
	} {
		t.Run(test.name, func(t *testing.T) {
			count := nativeCount(17)
			if err := count.UnmarshalJSON([]byte(test.wire)); err == nil || count != 17 {
				t.Fatalf("invalid count changed the receiver or was accepted: count=%d error=%v", count, err)
			}
		})
	}
}
