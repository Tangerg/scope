package keyword

import (
	"encoding/base64"
	"slices"

	"github.com/Tangerg/scope/core/metadata"
)

// Presence is emitted only for a present, non-null top-level value.
func Presence(key string) string { return "p:" + base64.RawURLEncoding.EncodeToString([]byte(key)) }

// String identifies one complete scalar string at an exact top-level key.
func String(key, value string) string {
	return "s:" + base64.RawURLEncoding.EncodeToString([]byte(key)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(value))
}

func Project(facts metadata.Map) ([]string, error) {
	values, err := facts.Values()
	if err != nil {
		return nil, err
	}
	// A static format term keeps the field present even when no metadata exists.
	terms := []string{"terms"}
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
