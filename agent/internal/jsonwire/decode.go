// Package jsonwire owns strict decoding for Agent protocols and persisted state.
package jsonwire

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
)

// Decode rejects unknown members, duplicate names and invalid UTF-8. Required
// members must be present and non-null; domain validation stays with the caller.
func Decode[T any](data []byte, required ...string) (T, error) {
	var value T
	if len(data) == 0 {
		return value, errors.New("JSON value is empty")
	}
	if err := jsonv2.Unmarshal(data, &value, jsonv2.RejectUnknownMembers(true)); err != nil {
		return value, err
	}
	if len(required) != 0 {
		var fields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(data, &fields); err != nil {
			return value, err
		}
		for _, name := range required {
			field, present := fields[name]
			if !present || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
				return value, fmt.Errorf("required JSON member %q is missing or null", name)
			}
		}
	}
	return value, nil
}
