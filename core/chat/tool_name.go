package chat

import (
	"fmt"
	"regexp"
	"strings"
)

const maxToolNameLength = 64

var toolNamePattern = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z0-9_-]{1,%d}$`, maxToolNameLength))

// SanitizeToolName projects a tool identity into the portable model namespace.
// The caller must reject an empty result or collisions within its exact binding.
func SanitizeToolName(label string) string {
	if toolNamePattern.MatchString(label) {
		return label
	}
	var b strings.Builder
	pendingUnderscore := false
	for _, r := range label {
		if b.Len() >= maxToolNameLength {
			break
		}
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
			pendingUnderscore = false
		default:
			if !pendingUnderscore {
				b.WriteByte('_')
				pendingUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func ValidateToolName(name string) error {
	if !toolNamePattern.MatchString(name) {
		return fmt.Errorf("%w: model tool name must match %s", ErrInvalidRequest, toolNamePattern)
	}
	return nil
}
