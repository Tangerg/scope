package image

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"time"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/timestamp"
)

const (
	binaryMediaType media.Type = "application/octet-stream"
)

type Output struct {
	Media *media.Media `json:"media,omitzero"`

	Metadata metadata.Map `json:"metadata,omitzero"`
}

// NewOutput validates and snapshots one provider result before it enters a Response.
func NewOutput(value *media.Media, outputMetadata metadata.Map) (*Output, error) {
	output := &Output{Media: value.Clone(), Metadata: outputMetadata.Clone()}
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
	if mediaType := o.Media.Type(); mediaType.TopLevel() != media.TopLevelImage && mediaType != binaryMediaType {
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
	return jsonv2.Marshal(wireOutput(o))
}

func (o *Output) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: output receiver is nil", ErrInvalidResponse)
	}
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

type ResponseMetadata struct {
	CreatedAt time.Time `json:"created_at,omitzero"`

	Extra metadata.Map `json:"extra,omitzero"`
}

func (r *ResponseMetadata) validate() error {
	if r == nil {
		return nil
	}
	if err := timestamp.Validate(r.CreatedAt); err != nil {
		return fmt.Errorf("%w: response metadata created_at: %w", ErrInvalidResponse, err)
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
	return jsonv2.Marshal(wireResponseMetadata(r))
}

func (r *ResponseMetadata) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: response metadata receiver is nil", ErrInvalidResponse)
	}
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

type Response struct {
	// Outputs contains every image returned by the provider, in provider order.
	Outputs []*Output `json:"outputs,omitzero"`

	Metadata *ResponseMetadata `json:"metadata,omitzero"`
}

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
	return jsonv2.Marshal(wireResponse(r))
}

func (r *Response) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: response receiver is nil", ErrInvalidResponse)
	}
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

type wireOutput Output

func (Output) JSONSchemaModel() any { return wireOutput{} }

type wireResponseMetadata ResponseMetadata

func (ResponseMetadata) JSONSchemaModel() any { return wireResponseMetadata{} }

type wireResponse Response

func (Response) JSONSchemaModel() any { return wireResponse{} }
