// Package modelid owns the admission rules shared by every modality's model
// identifier: an empty value is allowed, and a non-empty value must be valid
// UTF-8 without surrounding whitespace. Defaults may leave the model to the
// request, but resolved options must name one. Each modality wraps the
// returned error with its own sentinel so a request model and a response model
// keep distinct identities while sharing these rules.
package modelid

import (
	"errors"
	"strings"
	"unicode/utf8"
)

func Validate(id string) error {
	if !utf8.ValidString(id) {
		return errors.New("model must be valid UTF-8")
	}
	if id != "" && strings.TrimSpace(id) != id {
		return errors.New("model must not have surrounding whitespace")
	}
	return nil
}

// RequireResolved admits the model of resolved options, which every request
// is sent with, so it must be present.
func RequireResolved(id string) error {
	if id == "" {
		return errors.New("model is required in defaults or request options")
	}
	return nil
}
