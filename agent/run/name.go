package run

import (
	"fmt"
	"regexp"
)

const maxQualifiedNameBytes = 128

// QualifiedName is a lowercase dotted identifier carried on the wire. Valid
// owns its grammar; JSONSchemaPattern projects the same compiled grammar for
// schema generation.
type QualifiedName string

func (QualifiedName) JSONSchemaPattern() string { return qualifiedNamePattern.String() }

var qualifiedNamePattern = regexp.MustCompile(fmt.Sprintf(`^[a-z][a-z0-9._-]{0,%d}$`, maxQualifiedNameBytes-1))

func (q QualifiedName) Valid() bool { return qualifiedNamePattern.MatchString(string(q)) }
