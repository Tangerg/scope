package agent

const maxQualifiedNameBytes = 128

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
