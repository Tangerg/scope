package agent

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// MaxPayloadBytes is the maximum encoded JSON size of an individual Agent
// input, output, Effect, Signal, or settlement payload. Both the supplied bytes
// and the canonical encoding must fit. Canonical payloads use RFC 7493 JSON,
// omit whitespace, sort object names by UTF-16 code units (RFC 8785 section
// 3.2.3), preserve number literals verbatim, and escape <, >, &, U+2028 and
// U+2029. Strings otherwise use their shortest JSON encoding. Digests over
// payloads and persisted payload bytes use this form; this is not RFC 8785
// number canonicalization.
const MaxPayloadBytes = 64 << 20

var ErrInvalidPayload = errors.New("agent: invalid payload")

// Payload is an immutable canonical JSON value. Its zero value is invalid.
// ParsePayload and EncodePayload copy and normalize their input. Descriptor
// schemas define its role as Process input or final output; streamed Deltas
// never become final output implicitly.
type Payload struct {
	data json.RawMessage
}

// ParsePayload validates one JSON value and returns an independently owned Payload.
func ParsePayload(data json.RawMessage) (Payload, error) {
	normalized, err := normalizeJSON(data, MaxPayloadBytes)
	if err != nil {
		return Payload{}, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	return Payload{data: normalized}, nil
}

// EncodePayload strictly encodes a typed value into an independently owned Payload.
// Invalid UTF-8 and duplicate JSON names are rejected, including custom codec
// output. Custom codecs are responsible for preserving their source values.
func EncodePayload[T any](value T) (Payload, error) {
	data, err := jsonv2.Marshal(value, jsonv2.Deterministic(true))
	if err != nil {
		return Payload{}, fmt.Errorf("%w: encode: %w", ErrInvalidPayload, err)
	}
	return ParsePayload(data)
}

// Decode strictly decodes p into a typed value. Unknown object fields are
// rejected when T is a struct.
func (p Payload) Decode[T any]() (T, error) {
	value, err := jsonwire.Decode[T](p.data)
	if err != nil {
		return value, fmt.Errorf("%w: decode: %w", ErrInvalidPayload, err)
	}
	return value, nil
}

// JSON returns an independently owned JSON representation.
func (p Payload) JSON() json.RawMessage { return bytes.Clone(p.data) }

// IsZero distinguishes an absent optional payload from an explicit JSON null.
func (p Payload) IsZero() bool { return !p.Valid() }

func (p Payload) Valid() bool { return len(p.data) > 0 }

func (Payload) JSONSchemaAlias() any { return json.RawMessage{} }

func (p Payload) MarshalJSON() ([]byte, error) {
	if !p.Valid() {
		return nil, ErrInvalidPayload
	}
	return bytes.Clone(p.data), nil
}

func (p *Payload) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidPayload)
	}
	value, err := ParsePayload(data)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

func normalizeJSON(data []byte, limit int) (json.RawMessage, error) {
	if len(data) == 0 {
		return nil, errors.New("JSON value is empty")
	}
	if len(data) > limit {
		return nil, fmt.Errorf("JSON value exceeds %d bytes", limit)
	}
	normalized := jsontext.Value(bytes.Clone(data))
	if err := normalized.Format(jsontext.ReorderRawObjects(true), jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true)); err != nil {
		return nil, fmt.Errorf("JSON value is not valid RFC 7493 JSON: %w", err)
	}
	if len(normalized) > limit {
		return nil, fmt.Errorf("normalized JSON value exceeds %d bytes", limit)
	}
	return json.RawMessage(normalized), nil
}
