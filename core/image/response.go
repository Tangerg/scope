package image

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"mime"
	"slices"
	"strings"
	"time"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// Output is one generated image plus its metadata.
type Output struct {
	// Media holds the generated image as bytes or an absolute URI.
	Media *media.Media `json:"media,omitempty"`

	// Metadata carries per-image extras.
	Metadata metadata.Map `json:"metadata,omitzero"`
}

// NewOutput validates and snapshots one provider result before it enters a Response.
func NewOutput(value *media.Media, outputMetadata metadata.Map) (*Output, error) {
	output := &Output{Media: value, Metadata: outputMetadata.Clone()}
	if err := output.Validate(); err != nil {
		return nil, fmt.Errorf("image: create output: %w", err)
	}
	return output, nil
}

func (o *Output) Validate() error {
	if o == nil {
		return fmt.Errorf("%w: output must not be nil", ErrInvalidResponse)
	}
	if err := o.Media.Validate(); err != nil {
		return fmt.Errorf("%w: media: %w", ErrInvalidResponse, err)
	}
	mediaType, _, _ := mime.ParseMediaType(o.Media.MIME)
	if !strings.HasPrefix(mediaType, "image/") && mediaType != "application/octet-stream" {
		return fmt.Errorf("%w: media MIME type %q is not an image", ErrInvalidResponse, o.Media.MIME)
	}
	if err := o.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: output metadata: %w", ErrInvalidResponse, err)
	}
	return nil
}

func (o Output) MarshalJSON() ([]byte, error) {
	if err := (&o).Validate(); err != nil {
		return nil, err
	}
	type wireOutput Output
	return json.Marshal(wireOutput(o))
}

func (o *Output) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: output receiver is nil", ErrInvalidResponse)
	}
	type wireOutput Output
	var decoded wireOutput
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode output: %w", ErrInvalidResponse, err)
	}
	candidate := Output(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*o = candidate
	return nil
}

// ResponseMetadata holds response-level metadata for an image
// generation request.
type ResponseMetadata struct {
	// CreatedAt is the provider-reported creation timestamp.
	CreatedAt time.Time `json:"created_at,omitzero"`

	// Extra carries JSON-safe provider-specific metadata.
	Extra metadata.Map `json:"extra,omitzero"`
}

func (r *ResponseMetadata) validate() error {
	if r == nil {
		return nil
	}
	if err := r.Extra.Validate(); err != nil {
		return fmt.Errorf("%w: response metadata: %w", ErrInvalidResponse, err)
	}
	return nil
}

func (r ResponseMetadata) MarshalJSON() ([]byte, error) {
	if err := (&r).validate(); err != nil {
		return nil, err
	}
	type wireResponseMetadata ResponseMetadata
	return json.Marshal(wireResponseMetadata(r))
}

func (r *ResponseMetadata) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: response metadata receiver is nil", ErrInvalidResponse)
	}
	type wireResponseMetadata ResponseMetadata
	var decoded wireResponseMetadata
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode response metadata: %w", ErrInvalidResponse, err)
	}
	candidate := ResponseMetadata(decoded)
	if err := candidate.validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}

// Response is the full image-generation output: every rendered image plus
// shared response metadata.
type Response struct {
	// Outputs contains every image returned by the provider, in provider order.
	Outputs []*Output `json:"outputs,omitzero"`

	// Metadata carries shared response-level fields.
	Metadata *ResponseMetadata `json:"metadata,omitempty"`
}

// NewResponse validates a complete provider result at the protocol boundary.
func NewResponse(outputs []*Output, responseMetadata *ResponseMetadata) (*Response, error) {
	response := &Response{Outputs: slices.Clone(outputs), Metadata: responseMetadata}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("image: create response: %w", err)
	}
	return response, nil
}

func (r *Response) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil response", ErrInvalidResponse)
	}
	if len(r.Outputs) == 0 {
		return fmt.Errorf("%w: at least one output is required", ErrInvalidResponse)
	}
	for i, output := range r.Outputs {
		if err := output.Validate(); err != nil {
			return fmt.Errorf("%w: outputs[%d]: %w", ErrInvalidResponse, i, err)
		}
	}
	if err := r.Metadata.validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidResponse, err)
	}
	return nil
}

func (r *Response) First() *Output {
	if r == nil || len(r.Outputs) == 0 {
		return nil
	}
	return r.Outputs[0]
}

func (r Response) MarshalJSON() ([]byte, error) {
	if err := (&r).Validate(); err != nil {
		return nil, err
	}
	type wireResponse Response
	return json.Marshal(wireResponse(r))
}

func (r *Response) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: response receiver is nil", ErrInvalidResponse)
	}
	type wireResponse Response
	var decoded wireResponse
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode response: %w", ErrInvalidResponse, err)
	}
	candidate := Response(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}
