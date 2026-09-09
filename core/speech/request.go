package speech

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// Options holds provider-neutral text-to-speech configuration. Resolve overlays
// only explicitly supplied values, merges namespaced extensions, and never
// aliases mutable data from either input.
type Options struct {
	// Model is the provider model identifier (e.g. "tts-1").
	Model string `json:"model"`

	// Voice selects the speaker profile. Provider-specific values.
	Voice string `json:"voice"`

	// OutputFormat selects the audio container ("mp3", "wav", ...).
	OutputFormat string `json:"output_format"`

	// Speed scales the playback rate. 1.0 is normal speed.
	Speed float64 `json:"speed"`

	// Extensions carries JSON-safe provider-specific options unknown to this
	// struct.
	Extensions metadata.Extensions `json:"extensions,omitzero"`
}

func (o Options) Validate() error {
	if o.Model != "" && strings.TrimSpace(o.Model) != o.Model {
		return fmt.Errorf("%w: model id must not have surrounding whitespace", ErrInvalidOptions)
	}
	if math.IsNaN(o.Speed) || math.IsInf(o.Speed, 0) || o.Speed < 0 {
		return fmt.Errorf("%w: speed must be finite and non-negative", ErrInvalidOptions)
	}
	if err := o.Extensions.Validate(); err != nil {
		return fmt.Errorf("%w: extensions: %w", ErrInvalidOptions, err)
	}
	return nil
}

func (o Options) Clone() Options {
	return Options{
		Model:        o.Model,
		Voice:        o.Voice,
		OutputFormat: o.OutputFormat,
		Speed:        o.Speed,
		Extensions:   o.Extensions.Clone(),
	}
}

func (o Options) Resolve(override Options) (Options, error) {
	effective := o.Clone()
	if err := effective.applyOverride(override); err != nil {
		return Options{}, fmt.Errorf("speech: resolve options: %w: %w", ErrInvalidOptions, err)
	}
	if err := effective.Validate(); err != nil {
		return Options{}, fmt.Errorf("speech: resolve options: %w", err)
	}
	return effective, nil
}

func (o *Options) applyOverride(override Options) error {
	if override.Model != "" {
		o.Model = override.Model
	}
	if override.Voice != "" {
		o.Voice = override.Voice
	}
	if override.OutputFormat != "" {
		o.OutputFormat = override.OutputFormat
	}
	if override.Speed != 0 {
		o.Speed = override.Speed
	}
	if !override.Extensions.IsZero() {
		if err := o.Extensions.Merge(override.Extensions); err != nil {
			return fmt.Errorf("merge extensions: %w", err)
		}
	}
	return nil
}

func (o Options) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type wireOptions Options
	return json.Marshal(wireOptions(o))
}

func (o *Options) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: options receiver is nil", ErrInvalidOptions)
	}
	type wireOptions Options
	var decoded wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode options: %w", ErrInvalidOptions, err)
	}
	candidate := Options(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*o = candidate
	return nil
}

// Request is one TTS call: the input text and explicit options.
type Request struct {
	// Text is the prompt converted to speech.
	Text string `json:"text"`

	Options Options `json:"options,omitzero"`
}

// NewRequest validates the required input while leaving per-call options at
// their portable zero defaults.
func NewRequest(text string) (*Request, error) {
	r := &Request{Text: text}
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("speech: create request: %w", err)
	}
	return r, nil
}

func (r *Request) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	if r.Text == "" {
		return fmt.Errorf("%w: text must not be empty", ErrInvalidRequest)
	}
	if err := r.Options.Validate(); err != nil {
		return fmt.Errorf("%w: options: %w", ErrInvalidRequest, err)
	}
	return nil
}

func (r Request) MarshalJSON() ([]byte, error) {
	if err := (&r).Validate(); err != nil {
		return nil, err
	}
	type wireRequest Request
	return json.Marshal(wireRequest(r))
}

func (r *Request) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: request receiver is nil", ErrInvalidRequest)
	}
	type wireRequest Request
	var decoded wireRequest
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode request: %w", ErrInvalidRequest, err)
	}
	candidate := Request(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}
