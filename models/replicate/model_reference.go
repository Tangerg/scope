package replicate

import (
	"fmt"
	"strings"
)

const modelVersionLength = 64

// A model reference is body data, never a URL path or query. The binding
// validates it before retaining the corresponding Core default options.
type modelReference string

func (m modelReference) validate() error {
	value := string(m)
	if validModelVersion(value) {
		return nil
	}
	name, version, pinned := strings.Cut(value, ":")
	owner, model, qualified := strings.Cut(name, "/")
	if !qualified || !validModelName(owner) || !validModelName(model) || pinned && !validModelVersion(version) {
		return fmt.Errorf("replicate: invalid model reference %q: require owner/name, owner/name:64-character-version or 64-character-version", m)
	}
	return nil
}

func validModelVersion(value string) bool {
	if len(value) != modelVersionLength {
		return false
	}
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9', char >= 'a' && char <= 'f':
		default:
			return false
		}
	}
	return true
}

func validModelName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_', char == '.':
		default:
			return false
		}
	}
	return true
}
