// Package content preserves arbitrary tool-response bytes in a readable JSON
// representation. Valid UTF-8 remains text; other bytes use explicit base64.
package content

import (
	"bytes"
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"unicode/utf8"
)

var ErrInvalidContent = errors.New("tools/content: invalid encoded bytes")

// Content owns an immutable byte sequence. Its zero value is empty content.
// A UTF-8 prefix cut in the middle of a character remains recoverable bytes;
// encoding never replaces invalid bytes with a Unicode replacement character.
type Content struct {
	data []byte
}

func New(data []byte) Content { return Content{data: bytes.Clone(data)} }

func (c Content) Bytes() []byte { return bytes.Clone(c.data) }

// Text reports whether the entire content is valid UTF-8. It never guesses an
// encoding or repairs a truncated sequence.
func (c Content) Text() (string, bool) {
	if !utf8.Valid(c.data) {
		return "", false
	}
	return string(c.data), true
}

func (c Content) MarshalJSON() ([]byte, error) {
	format := encodingUTF8
	data, valid := c.Text()
	if !valid {
		format = encodingBase64
		data = base64.StdEncoding.EncodeToString(c.data)
	}
	return jsonv2.Marshal(contentWire{Encoding: format, Data: &data})
}

func (c *Content) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidContent)
	}
	var wire contentWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidContent, err)
	}
	if wire.Data == nil {
		return fmt.Errorf("%w: data is required", ErrInvalidContent)
	}
	var decoded []byte
	switch wire.Encoding {
	case encodingUTF8:
		decoded = []byte(*wire.Data)
	case encodingBase64:
		var err error
		decoded, err = base64.StdEncoding.Strict().DecodeString(*wire.Data)
		if err != nil {
			return fmt.Errorf("%w: decode base64 data: %w", ErrInvalidContent, err)
		}
		if base64.StdEncoding.EncodeToString(decoded) != *wire.Data {
			return fmt.Errorf("%w: data must use canonical base64", ErrInvalidContent)
		}
		if utf8.Valid(decoded) {
			return fmt.Errorf("%w: valid UTF-8 must use utf8 encoding", ErrInvalidContent)
		}
	default:
		return fmt.Errorf("%w: unsupported encoding %q", ErrInvalidContent, wire.Encoding)
	}
	c.data = decoded
	return nil
}

type encoding string

const (
	encodingUTF8   encoding = "utf8"
	encodingBase64 encoding = "base64"
)

type contentWire struct {
	Encoding encoding `json:"encoding"`
	Data     *string  `json:"data"`
}
