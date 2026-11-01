package tool

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

var ErrInvalidRequest = errors.New("tool: invalid request")

// Request is an untrusted proposal. A provider correlation ID belongs to its
// transport; execution requires admission by the exact frozen Contract.
type Request struct {
	Name      string
	Arguments string
}

func (r Request) Validate() error {
	if r.Name == "" || !utf8.ValidString(r.Name) || !utf8.ValidString(r.Arguments) {
		return fmt.Errorf("%w: name must be non-empty and name and arguments must be valid UTF-8", ErrInvalidRequest)
	}
	return nil
}
