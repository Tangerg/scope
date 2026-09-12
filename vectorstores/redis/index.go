package redis

import (
	"encoding/json"
	"fmt"
)

// formatMetadataValue coerces a Go value into the HASH string form
// RediSearch can index. Slices and maps are JSON-encoded — they only
// matter when the caller stored them as TEXT fields.
//
// The composite branch reports a marshal failure instead of substituting
// fmt.Sprint. Go's %v rendering of a map or slice is not JSON, so the
// substitution wrote a value no reader can decode while telling the caller the
// document was stored as given.
func formatMetadataValue(v any) (any, error) {
	switch val := v.(type) {
	case nil:
		return "", nil
	case string, int, int64, float32, float64, bool:
		return val, nil
	case []byte:
		return val, nil
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return nil, fmt.Errorf("redis: encode metadata value of type %T: %w", val, err)
		}
		return string(b), nil
	}
}
