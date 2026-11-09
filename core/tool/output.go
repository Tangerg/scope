package tool

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	corecontent "github.com/Tangerg/scope/core/content"
)

var ErrInvalidOutput = errors.New("tool: invalid output")

// Output is the provider-neutral value produced by a Tool. Content is the
// model-visible ordered text/media representation. Details is optional JSON
// for structured consumers; providers use its encoded JSON as the model-visible
// fallback only when Content is empty.
type Output struct {
	Content []corecontent.Block `json:"content,omitempty"`
	Details json.RawMessage     `json:"details,omitzero"`
}

// NewTextOutput returns a text output. Empty text is represented by the
// valid zero Output rather than an invalid empty text Part.
func NewTextOutput(text string) Output {
	if text == "" {
		return Output{}
	}
	return Output{Content: []corecontent.Block{{Kind: corecontent.Text, Text: text}}}
}

// NewJSONOutput returns a structured output whose exact JSON encoding is
// preserved. The value must be one complete RFC 7493 JSON document.
func NewJSONOutput(value json.RawMessage) (Output, error) {
	if len(value) == 0 {
		return Output{}, fmt.Errorf("%w: details must not be empty", ErrInvalidOutput)
	}
	output := Output{Details: bytes.Clone(value)}
	if err := output.Validate(); err != nil {
		return Output{}, err
	}
	return output, nil
}

func (o Output) Clone() Output {
	clone := Output{Details: bytes.Clone(o.Details)}
	if o.Content != nil {
		clone.Content = make([]corecontent.Block, len(o.Content))
		for index := range o.Content {
			clone.Content[index] = o.Content[index].Clone()
		}
	}
	return clone
}

func (o Output) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(wireOutput(o), jsonv2.Deterministic(true))
}

func (o *Output) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidOutput)
	}
	var decoded wireOutput
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidOutput, err)
	}
	candidate := Output(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*o = candidate
	return nil
}

func (o Output) Validate() error {
	for index := range o.Content {
		part := o.Content[index]
		if err := part.Validate(); err != nil {
			return fmt.Errorf("%w: content[%d]: %w", ErrInvalidOutput, index, err)
		}
	}
	if o.Details != nil && !jsontext.Value(o.Details).IsValid() {
		return fmt.Errorf("%w: details must be one valid RFC 7493 JSON document", ErrInvalidOutput)
	}
	return nil
}

// Text returns the lossless text projection used by providers whose tool
// result protocol accepts only strings. It reports false when Content contains
// media so adapters cannot silently discard it. Details is encoded only when
// Content is empty.
func (o Output) Text() (string, bool) {
	if len(o.Content) == 0 {
		return string(o.Details), true
	}
	var projected strings.Builder
	for index := range o.Content {
		if o.Content[index].Kind != corecontent.Text {
			return "", false
		}
		projected.WriteString(o.Content[index].Text)
	}
	return projected.String(), true
}

type wireOutput Output

func (Output) JSONSchemaModel() any { return wireOutput{} }
