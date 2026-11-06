package document

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// SourceName can be admitted before reading the bytes that establish a
// revision. Its empty value denotes an anonymous source.
type SourceName string

func (s SourceName) Validate() error {
	if !validProvenanceText(string(s)) {
		return fmt.Errorf("%w: invalid source name", ErrInvalidDocument)
	}
	return nil
}

func validProvenanceText(value string) bool {
	return value == "" || (utf8.ValidString(value) && strings.TrimSpace(value) != "")
}
