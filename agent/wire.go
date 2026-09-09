package agent

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
)

const maxWireBytes = 64 << 20

var (
	ErrInvalidInput  = errors.New("agent: invalid input")
	ErrInvalidOutput = errors.New("agent: invalid output")
)

// Input is the immutable JSON value used to start a Process. Its zero value is
// invalid. ParseInput and EncodeInput take ownership by copying and normalizing
// their input.
type Input struct {
	data json.RawMessage
}

// ParseInput validates one JSON value and returns an independently owned Input.
func ParseInput(data json.RawMessage) (Input, error) {
	normalized, err := wireJSON.normalize(data, maxWireBytes)
	if err != nil {
		return Input{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return Input{data: normalized}, nil
}

// EncodeInput converts a typed value into an independently owned Input.
func EncodeInput[T any](value T) (Input, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Input{}, fmt.Errorf("%w: encode: %w", ErrInvalidInput, err)
	}
	return ParseInput(data)
}

// Decode strictly decodes i into a typed value. Unknown object fields are
// rejected when T is a struct.
func (i Input) Decode[T any]() (T, error) {
	value, err := wireJSON.decode[T](i.data)
	if err != nil {
		return value, fmt.Errorf("%w: decode: %w", ErrInvalidInput, err)
	}
	return value, nil
}

// JSON returns an independently owned JSON representation.
func (i Input) JSON() json.RawMessage { return bytes.Clone(i.data) }

func (i Input) Valid() bool { return len(i.data) > 0 }

func (Input) JSONSchemaAlias() any { return json.RawMessage{} }

func (i Input) MarshalJSON() ([]byte, error) {
	if !i.Valid() {
		return nil, ErrInvalidInput
	}
	return bytes.Clone(i.data), nil
}

func (i *Input) UnmarshalJSON(data []byte) error {
	if i == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidInput)
	}
	value, err := ParseInput(data)
	if err != nil {
		return err
	}
	*i = value
	return nil
}

// Output is the immutable final semantic result of a completed Process. Its
// zero value is invalid and it never represents streamed Delta content.
type Output struct {
	data json.RawMessage
}

// ParseOutput validates one JSON value and returns an independently owned Output.
func ParseOutput(data json.RawMessage) (Output, error) {
	normalized, err := wireJSON.normalize(data, maxWireBytes)
	if err != nil {
		return Output{}, fmt.Errorf("%w: %w", ErrInvalidOutput, err)
	}
	return Output{data: normalized}, nil
}

// EncodeOutput converts a typed value into an independently owned Output.
func EncodeOutput[T any](value T) (Output, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Output{}, fmt.Errorf("%w: encode: %w", ErrInvalidOutput, err)
	}
	return ParseOutput(data)
}

// Decode strictly decodes o into a typed value. Unknown object fields are
// rejected when T is a struct.
func (o Output) Decode[T any]() (T, error) {
	value, err := wireJSON.decode[T](o.data)
	if err != nil {
		return value, fmt.Errorf("%w: decode: %w", ErrInvalidOutput, err)
	}
	return value, nil
}

// JSON returns an independently owned JSON representation.
func (o Output) JSON() json.RawMessage { return bytes.Clone(o.data) }

func (o Output) Valid() bool { return len(o.data) > 0 }

func (Output) JSONSchemaAlias() any { return json.RawMessage{} }

func (o Output) MarshalJSON() ([]byte, error) {
	if !o.Valid() {
		return nil, ErrInvalidOutput
	}
	return bytes.Clone(o.data), nil
}

func (o *Output) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidOutput)
	}
	value, err := ParseOutput(data)
	if err != nil {
		return err
	}
	*o = value
	return nil
}

type jsonCodec struct{}

var wireJSON jsonCodec

func (jsonCodec) normalize(data []byte, limit int) (json.RawMessage, error) {
	if len(data) == 0 {
		return nil, errors.New("JSON value is empty")
	}
	if len(data) > limit {
		return nil, fmt.Errorf("JSON value exceeds %d bytes", limit)
	}
	if !jsontext.Value(data).IsValid() {
		return nil, errors.New("JSON value is not valid RFC 7493 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON value: %w", err)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("normalize JSON value: %w", err)
	}
	if len(normalized) > limit {
		return nil, fmt.Errorf("normalized JSON value exceeds %d bytes", limit)
	}
	return normalized, nil
}

func (jsonCodec) decode[T any](data []byte) (T, error) {
	var value T
	if len(data) == 0 {
		return value, errors.New("JSON value is empty")
	}
	if err := jsonv2.Unmarshal(data, &value, jsonv2.RejectUnknownMembers(true)); err != nil {
		return value, err
	}
	return value, nil
}
