package pgvector

import (
	"fmt"
	"regexp"
)

const maxIdentifierBytes = 63

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type identifier string

func (i identifier) validate(field string) error {
	if len(i) > maxIdentifierBytes {
		return fmt.Errorf("pgvector: %s exceeds PostgreSQL identifier limit of %d bytes", field, maxIdentifierBytes)
	}
	if !identifierPattern.MatchString(string(i)) {
		return fmt.Errorf("pgvector: %s=%q must match %s", field, i, identifierPattern)
	}
	return nil
}
