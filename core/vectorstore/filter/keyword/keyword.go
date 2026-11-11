package keyword

import (
	"encoding/base64"
	"slices"

	"github.com/Tangerg/scope/core/metadata"
)

const (
	presencePrefix = "p:"
	stringPrefix   = "s:"
	separator      = ":"
	// formatTerm keeps the field present even when no metadata exists.
	formatTerm = "terms"
)

// Presence is emitted only for a present, non-null top-level value.
func Presence(key string) string { return presencePrefix + encode(key) }

// String identifies one complete scalar string at an exact top-level key.
func String(key, value string) string { return stringPrefix + encode(key) + separator + encode(value) }

func encode(text string) string { return base64.RawURLEncoding.EncodeToString([]byte(text)) }

func Project(facts metadata.Map) ([]string, error) {
	values, err := facts.Values()
	if err != nil {
		return nil, err
	}
	terms := []string{formatTerm}
	for k, v := range values {
		if v != nil {
			terms = append(terms, Presence(k))
		}
		if str, ok := v.(string); ok {
			terms = append(terms, String(k, str))
		}
	}
	slices.Sort(terms)
	return terms, nil
}
