package textread_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/tools/textread"
)

func TestValidateAdmitsCompleteEditableText(t *testing.T) {
	const longPrefixBytes = 16 << 10
	for _, test := range []struct {
		name, input string
		invalid     bool
	}{
		{name: "empty"},
		{name: "unicode", input: "héllo 世界\n"},
		{name: "format markers", input: "\xef\xbb\xbfhello\r\n"},
		{name: "invalid UTF-8", input: "\xff", invalid: true},
		{name: "incomplete UTF-8", input: "\xe4\xb8", invalid: true},
		{name: "NUL after long text", input: strings.Repeat("a", longPrefixBytes) + "\x00", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := textread.Validate([]byte(test.input))
			if test.invalid && !errors.Is(err, textread.ErrInvalidText) || !test.invalid && err != nil {
				t.Fatalf("Validate = %v, invalid = %v", err, test.invalid)
			}
		})
	}
}
