// Package modelid owns the one admission rule shared by every modality's model
// identifier: an empty value is allowed, and a non-empty value must be valid
// UTF-8 without surrounding whitespace. Each modality wraps the returned error
// with its own sentinel so a request model and a response model keep distinct
// identities while sharing this rule.
package modelid

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Validate enforces the shared model identifier rule.
func Validate(id string) error {
	if !utf8.ValidString(id) {
		return errors.New("model must be valid UTF-8")
	}
	if id != "" && strings.TrimSpace(id) != id {
		return errors.New("model must not have surrounding whitespace")
	}
	return nil
}
