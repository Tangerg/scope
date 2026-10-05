package cassandra

import (
	"fmt"
	"regexp"
	"strings"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type identifier string

func (i identifier) validate(field string) error {
	if !identifierPattern.MatchString(string(i)) {
		return fmt.Errorf("cassandra: %s=%q must match %s", field, i, identifierPattern)
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
