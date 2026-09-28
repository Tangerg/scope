package textread

import (
	"strings"
	"testing"
)

func TestScanPreservesCarriageReturnOutsideCRLF(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"value\r", "value\r"},
		{"value\r\n", "value\n"},
		{"value\r\r\n", "value\r\n"},
		{"first\r\nlast\r", "first\nlast\r"},
		{"\xef\xbb\xbfvalue\r", "value\r"},
	} {
		t.Run(test.input, func(t *testing.T) {
			result, err := Scan(t.Context(), strings.NewReader(test.input), Options{InputBytes: 128, LineBytes: 64, OutputBytes: 128})
			if err != nil || result.Content != test.want {
				t.Fatalf("Scan(%q) = %q, %v; want %q", test.input, result.Content, err, test.want)
			}
		})
	}
}
