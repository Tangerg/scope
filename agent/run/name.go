package run

import (
	"fmt"
	"regexp"
)

const maxQualifiedNameBytes = 128

// QualifiedName is a lowercase dotted identifier carried on the wire. Its value
// is validated by ValidQualifiedName; JSONSchemaPattern projects that same rule
// for schema generation from the same compiled grammar.
type QualifiedName string

func (QualifiedName) JSONSchemaPattern() string { return qualifiedNamePattern.String() }

var qualifiedNamePattern = regexp.MustCompile(fmt.Sprintf(`^[a-z][a-z0-9._-]{0,%d}$`, maxQualifiedNameBytes-1))

func ValidQualifiedName(name string) bool { return qualifiedNamePattern.MatchString(name) }
