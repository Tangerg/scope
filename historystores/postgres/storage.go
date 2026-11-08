package postgres

import (
	"regexp"
)

// PostgreSQL silently truncates longer identifiers on standard builds.
const maxIdentifierBytes = 63

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validIdentifier(value string) bool {
	return len(value) <= maxIdentifierBytes && identifierPattern.MatchString(value)
}
