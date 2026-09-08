package redis

import (
	"fmt"
	"strings"
)

// fieldIdentifier is a HASH field name this store writes into a RediSearch
// query as text.
//
// Every configured field name reaches the query language: the schema declares
// it in FT.CREATE and the filter visitor emits it as `@name`. RediSearch has no
// way to quote a field name, so a name carrying the query language's own
// syntax — `}`, `|`, `@`, a space — would be read as syntax rather than as a
// name. The sibling stores that interpolate configured column names already
// require an identifier of them; this store interpolated without asking.
type fieldIdentifier string

// validate accepts a dot-separated path of plain identifiers. The dots matter:
// a RediSearch schema is flat, so this store declares a nested metadata key as
// a dotted field name, and rejecting the dot would reject the only way to
// filter one.
func (f fieldIdentifier) validate(field string) error {
	if f == "" {
		return fmt.Errorf("redis: %s must not be empty", field)
	}
	for _, segment := range strings.Split(string(f), ".") {
		if !plainIdentifier(segment) {
			return fmt.Errorf(
				"redis: %s=%q must be a dot-separated path of identifiers (letter or underscore, then letters, digits or underscores)",
				field, f)
		}
	}
	return nil
}

func plainIdentifier(segment string) bool {
	if segment == "" {
		return false
	}
	for index, character := range segment {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character == '_':
		case character >= '0' && character <= '9':
			if index == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
