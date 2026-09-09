package embedding

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/internal/ptr"
	"github.com/Tangerg/scope/core/metadata"
)

// Options holds per-request configuration for an embedding call. Pointer
// fields use nil to preserve the distinction between an override and a provider
// default. Resolve snapshots mutable values and overlays only fields explicitly
// supplied by the request.
type Options struct {
	// Model is the provider model identifier
	// (e.g. "text-embedding-3-small").
	Model string `json:"model"`

	// Dimensions requests an explicit output vector size. nil leaves it
	// up to the provider's default.
	Dimensions *int64 `json:"dimensions,omitempty"`

	// Extensions carries JSON-safe provider-specific options unknown to this
	// struct.
	Extensions metadata.Extensions `json:"extensions,omitzero"`
}

func (o Options) Clone() Options {
	return Options{
		Model:      o.Model,
		Dimensions: ptr.Clone(o.Dimensions),
		Extensions: o.Extensions.Clone(),
	}
}

func (o Options) Resolve(override Options) (Options, error) {
	effective := o.Clone()
	if err := effective.applyOverride(override); err != nil {
		return Options{}, fmt.Errorf("embedding: resolve options: %w: %w", ErrInvalidOptions, err)
	}
	if err := effective.Validate(); err != nil {
		return Options{}, fmt.Errorf("embedding: resolve options: %w", err)
	}
	return effective, nil
}

func (o *Options) applyOverride(override Options) error {
	if override.Model != "" {
		o.Model = override.Model
	}
	if override.Dimensions != nil {
		o.Dimensions = ptr.Clone(override.Dimensions)
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
	if o.Dimensions != nil && *o.Dimensions <= 0 {
		return fmt.Errorf("%w: dimensions must be positive", ErrInvalidOptions)
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

// Request is one embedding call: the input texts and explicit options.
type Request struct {
	// Texts is the input list. Each entry produces one embedding.
	Texts []string `json:"texts,omitzero"`

	Options Options `json:"options,omitzero"`
}

// NewRequest preserves the provider-neutral batch shape and clones the input,
// so later caller mutation cannot change a request already in flight.
func NewRequest(texts []string) (*Request, error) {
	r := &Request{Texts: slices.Clone(texts)}
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("embedding: create request: %w", err)
	}
	return r, nil
}

func (r *Request) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	if len(r.Texts) == 0 {
		return fmt.Errorf("%w: texts must contain at least one entry", ErrInvalidRequest)
	}
	for i, text := range r.Texts {
		if text == "" {
			return fmt.Errorf("%w: texts[%d] must not be empty", ErrInvalidRequest, i)
		}
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
