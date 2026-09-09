package transcription

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// Options holds provider-neutral transcription configuration. Provider-specific
// controls belong in Extensions. Resolve overlays only explicitly supplied
// values and snapshots extension data, leaving both inputs unchanged.
type Options struct {
	// Model is the provider model identifier (e.g. "whisper-1").
	Model string `json:"model"`

	// Language is an ISO-639-1 language code (e.g. "en", "zh") hinting
	// the spoken language. Empty leaves detection to the provider.
	Language string `json:"language"`

	// Extensions carries JSON-safe provider-specific options unknown to this
	// struct.
	Extensions metadata.Extensions `json:"extensions,omitzero"`
}

func (o Options) Clone() Options {
	return Options{
		Model:      o.Model,
		Language:   o.Language,
		Extensions: o.Extensions.Clone(),
	}
}

func (o Options) Resolve(override Options) (Options, error) {
	effective := o.Clone()
	if err := effective.applyOverride(override); err != nil {
		return Options{}, fmt.Errorf("transcription: resolve options: %w: %w", ErrInvalidOptions, err)
	}
	if err := effective.Validate(); err != nil {
		return Options{}, fmt.Errorf("transcription: resolve options: %w", err)
	}
	return effective, nil
}

func (o *Options) applyOverride(override Options) error {
	if override.Model != "" {
		o.Model = override.Model
	}
	if override.Language != "" {
		o.Language = override.Language
	}
	if !override.Extensions.IsZero() {
		if err := o.Extensions.Merge(override.Extensions); err != nil {
			return fmt.Errorf("merge extensions: %w", err)
		}
	}
	return nil
}

func (o Options) Validate() error {
	if o.Model != "" && strings.TrimSpace(o.Model) != o.Model {
		return fmt.Errorf("%w: model id must not have surrounding whitespace", ErrInvalidOptions)
	}
	if o.Language != "" && strings.TrimSpace(o.Language) != o.Language {
		return fmt.Errorf("%w: language must not have surrounding whitespace", ErrInvalidOptions)
	}
	if err := o.Extensions.Validate(); err != nil {
		return fmt.Errorf("%w: extensions: %w", ErrInvalidOptions, err)
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

// Request is one transcription call: the audio payload and explicit options.
type Request struct {
	// Audio carries the audio bytes (or URL) to transcribe.
	Audio *media.Media `json:"audio,omitempty"`

	Options Options `json:"options,omitzero"`
}

// NewRequest validates the required input while leaving per-call options at
// their portable zero defaults.
func NewRequest(audio *media.Media) (*Request, error) {
	r := &Request{Audio: audio}
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("transcription: create request: %w", err)
	}
	return r, nil
}

func (r *Request) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	if r.Audio == nil {
		return fmt.Errorf("%w: audio must not be nil", ErrInvalidRequest)
	}
	if err := r.Audio.Validate(); err != nil {
		return fmt.Errorf("%w: audio: %w", ErrInvalidRequest, err)
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
