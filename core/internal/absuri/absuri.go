// Package absuri owns the one rule Core applies wherever a value must be an
// absolute URI: it parses, carries a scheme, names a non-empty target, and
// contains no whitespace. RFC 3986 admits no whitespace in a URI even where
// url.Parse tolerates it, so a value one Core type accepts is never rejected
// by another.
package absuri

import (
	"net/url"
	"strings"
	"unicode"
)

func Valid(value string) bool {
	if strings.ContainsFunc(value, unicode.IsSpace) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.IsAbs() && (parsed.Opaque != "" || parsed.Host != "" || parsed.Path != "")
}
