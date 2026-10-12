package agent

const maxQualifiedNameBytes = 128

// QualifiedNamePattern is the wire projection of ValidQualifiedName for JSON
// Schema. The two forms are guarded against drift by TestQualifiedNamePattern.
const QualifiedNamePattern = `^[a-z][a-z0-9._-]{0,127}$`

// QualifiedName is a lowercase dotted identifier carried on the wire. Containing
// types validate the value through ValidQualifiedName; the schema derives its
// pattern from the same rule.
type QualifiedName string

func (QualifiedName) JSONSchemaPattern() string { return QualifiedNamePattern }

// ValidQualifiedName checks a 1–128 byte name beginning with a lowercase ASCII
// letter and followed only by lowercase letters, digits, dots, underscores or hyphens.
func ValidQualifiedName(name string) bool {
	if len(name) == 0 || len(name) > maxQualifiedNameBytes || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
