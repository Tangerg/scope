package oracle

import (
	"fmt"
	"regexp"
	"strings"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type identifier string

func (i identifier) validate(field string) error {
	if !identifierPattern.MatchString(string(i)) {
		return fmt.Errorf("oracle: %s=%q must match %s", field, i, identifierPattern)
	}
	return nil
}

func (i identifier) name() string {
	return strings.ToUpper(string(i))
}

func (i identifier) sql() string {
	return `"` + i.name() + `"`
}
