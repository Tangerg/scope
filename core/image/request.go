package image

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"mime"
	"strings"

	"github.com/Tangerg/scope/core/internal/ptr"
	"github.com/Tangerg/scope/core/metadata"
)

// Options holds per-request configuration for an image-generation call.
// Pointer fields preserve the distinction between an override and a provider
// default. Resolve snapshots mutable values and overlays only fields explicitly
// supplied by the request.
type Options struct {
	// Model is the provider model identifier (e.g. "dall-e-3").
	Model string `json:"model"`

	// NegativePrompt describes what should not appear in the image.
	NegativePrompt string `json:"negative_prompt"`

	// Width / Height set the output dimensions in pixels.
	Width  *int64 `json:"width,omitempty"`
	Height *int64 `json:"height,omitempty"`

	// Seed pins the RNG so repeated calls produce the same image.
	Seed *int64 `json:"seed,omitempty"`

	// OutputFormat picks the image MIME type of the rendered bytes.
	// Empty leaves the format to the provider.
	OutputFormat string `json:"output_format,omitempty"`

	// Extensions carries JSON-safe provider-specific options unknown to this
	// struct.
	Extensions metadata.Extensions `json:"extensions,omitzero"`
}

func (o Options) Clone() Options {
	return Options{
		Model:          o.Model,
		NegativePrompt: o.NegativePrompt,
		Width:          ptr.Clone(o.Width),
		Height:         ptr.Clone(o.Height),
		Seed:           ptr.Clone(o.Seed),
		OutputFormat:   o.OutputFormat,
		Extensions:     o.Extensions.Clone(),
	}
}

func (o Options) Resolve(override Options) (Options, error) {
	effective := o.Clone()
	if err := effective.applyOverride(override); err != nil {
		return Options{}, fmt.Errorf("image: resolve options: %w: %w", ErrInvalidOptions, err)
	}
	if err := effective.Validate(); err != nil {
		return Options{}, fmt.Errorf("image: resolve options: %w", err)
	}
	return effective, nil
}

func (o Options) validateOutputFormat() error {
	if o.OutputFormat == "" {
		return nil
	}
	mediaType, parameters, err := mime.ParseMediaType(o.OutputFormat)
	if err != nil {
		return fmt.Errorf("invalid output format %q: %w", o.OutputFormat, err)
	}
	canonical := strings.ToLower(mediaType)
	if !strings.HasPrefix(canonical, "image/") || len(strings.TrimPrefix(canonical, "image/")) == 0 {
		return fmt.Errorf("output format %q is not an image MIME type", o.OutputFormat)
	}
	if len(parameters) != 0 {
		return fmt.Errorf("output format %q must not include parameters", o.OutputFormat)
	}
	if canonical != o.OutputFormat {
		return fmt.Errorf("output format must use canonical MIME form %q", canonical)
	}
	return nil
}

func (o *Options) applyOverride(override Options) error {
	if override.NegativePrompt != "" {
		o.NegativePrompt = override.NegativePrompt
	}
	if override.Model != "" {
		o.Model = override.Model
	}
	if override.Width != nil {
		o.Width = ptr.Clone(override.Width)
	}
	if override.Height != nil {
		o.Height = ptr.Clone(override.Height)
	}
	if override.Seed != nil {
		o.Seed = ptr.Clone(override.Seed)
	}
	if override.OutputFormat != "" {
		o.OutputFormat = override.OutputFormat
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
	if o.Width != nil && *o.Width <= 0 {
		return fmt.Errorf("%w: width must be positive", ErrInvalidOptions)
	}
	if o.Height != nil && *o.Height <= 0 {
		return fmt.Errorf("%w: height must be positive", ErrInvalidOptions)
	}
	if o.Seed != nil && *o.Seed < 0 {
		return fmt.Errorf("%w: seed must not be negative", ErrInvalidOptions)
	}
	if err := o.validateOutputFormat(); err != nil {
		return fmt.Errorf("%w: output format: %w", ErrInvalidOptions, err)
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

// Request is one image-generation call: the prompt and explicit options.
type Request struct {
	// Prompt is the natural-language description of the desired image.
	Prompt string `json:"prompt"`

	Options Options `json:"options,omitzero"`
}

// NewRequest validates the required input while leaving per-call options at
// their portable zero defaults.
func NewRequest(prompt string) (*Request, error) {
	r := &Request{Prompt: prompt}
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("image: create request: %w", err)
	}
	return r, nil
}

func (r *Request) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	if r.Prompt == "" {
		return fmt.Errorf("%w: prompt must not be empty", ErrInvalidRequest)
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
