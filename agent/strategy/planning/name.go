package planning

import (
	"strings"
	"unicode/utf8"
)

const maxDescriptionBytes = 4096

func validDescription(description string) bool {
	return description != "" && len(description) <= maxDescriptionBytes &&
		utf8.ValidString(description) && strings.TrimSpace(description) == description
}
