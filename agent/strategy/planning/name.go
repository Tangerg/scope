package planning

import (
	"strings"
	"unicode/utf8"
)

const (
	maxNameBytes        = 128
	maxDescriptionBytes = 4096
)

func validName(name string) bool {
	if len(name) == 0 || len(name) > maxNameBytes || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validDescription(description string) bool {
	return description != "" && len(description) <= maxDescriptionBytes &&
		utf8.ValidString(description) && strings.TrimSpace(description) == description
}
