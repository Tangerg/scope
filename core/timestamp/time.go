// Package timestamp owns lossless RFC 3339 admission for protocol timestamps.
package timestamp

import (
	"errors"
	"time"
)

// Validate accepts omitted zero times or timestamps that JSON can preserve.
// Optional timestamp fields must use omitzero; the zero instant is treated as
// absent even when its location has an otherwise unrepresentable offset.
func Validate(value time.Time) error {
	if value.IsZero() {
		return nil
	}
	if _, err := value.MarshalJSON(); err != nil {
		return err
	}
	// RFC 3339 records offsets in minutes; Go silently truncates offset seconds.
	_, offset := value.Zone()
	if offset%int(time.Minute/time.Second) != 0 {
		return errors.New("timestamp zone offset must use whole minutes for lossless RFC 3339 encoding")
	}
	return nil
}
