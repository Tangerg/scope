package textread

import (
	"bytes"
	"errors"
	"unicode/utf8"
)

const nulByte = '\x00'

var ErrInvalidText = errors.New("textread: input must be valid UTF-8 without NUL")

// Validate admits complete text before scanning or editing. UTF-8 alone also
// permits NUL, which filesystem adapters cannot expose as editable text.
func Validate(text []byte) error {
	if !utf8.Valid(text) || bytes.IndexByte(text, nulByte) >= 0 {
		return ErrInvalidText
	}
	return nil
}
