package httpclient

import (
	"net/url"
	"strings"
)

const (
	dotSegment        = "."
	parentDotSegment  = ".."
	encodedDotSegment = "%2E"
)

// PathSegment encodes value as exactly one URL path segment. Escaping alone
// leaves "." and ".." literal, and reference resolution would then drop the
// segment or climb to its parent, so their dots are percent-encoded too. An
// empty value encodes to an empty segment; callers own presence validation.
func PathSegment(value string) string {
	if value == dotSegment || value == parentDotSegment {
		return strings.Repeat(encodedDotSegment, len(value))
	}
	return url.PathEscape(value)
}
