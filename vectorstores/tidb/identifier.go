package tidb

import (
	"fmt"
	"regexp"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type identifier string

func (i identifier) validate(field string) error {
	if !identifierPattern.MatchString(string(i)) {
		return fmt.Errorf("tidb: %s=%q must match %s", field, i, identifierPattern)
	}
	return nil
}

func (i identifier) sql() string {
	return "`" + string(i) + "`"
}
