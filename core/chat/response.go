package chat

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tangerg/scope/core/metadata"
)

// ErrInvalidResponse identifies provider output that cannot satisfy the
// portable response contract.
var ErrInvalidResponse = errors.New("chat: invalid response")

// ResponseMetadata holds provider identity, usage, and response-scoped extras.
type ResponseMetadata struct {
	ID    string `json:"id,omitempty"`
	Model string `json:"model,omitempty"`
	// Usage is nil when token accounting was not reported. A non-nil zero
	// value records an explicitly reported zero total.
	Usage     *Usage       `json:"usage,omitempty"`
	CreatedAt time.Time    `json:"created_at,omitzero"`
	Extra     metadata.Map `json:"extra,omitzero"`
}

func (r *ResponseMetadata) validate() error {
	if r == nil {
		return nil
	}
	if r.ID != "" && strings.TrimSpace(r.ID) != r.ID {
		return fmt.Errorf("%w: response metadata ID must not have surrounding whitespace", ErrInvalidResponse)
	}
	if r.Model != "" && strings.TrimSpace(r.Model) != r.Model {
		return fmt.Errorf("%w: response metadata model must not have surrounding whitespace", ErrInvalidResponse)
	}
	if r.Usage != nil {
		if err := r.Usage.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidResponse, err)
		}
	}
	if err := r.Extra.Validate(); err != nil {
		return fmt.Errorf("%w: response metadata: %w", ErrInvalidResponse, err)
	}
	return nil
}

func (r *ResponseMetadata) mergeValidated(src ResponseMetadata) {
	if src.ID != "" {
		r.ID = src.ID
	}
	if src.Model != "" {
		r.Model = src.Model
	}
	if src.Usage != nil {
		r.Usage = new(src.Usage.clone())
	}
	if !src.CreatedAt.IsZero() {
		r.CreatedAt = src.CreatedAt
	}
	mergeValidatedMetadata(&r.Extra, src.Extra)
}

func (r ResponseMetadata) clone() *ResponseMetadata {
	clone := r
	if r.Usage != nil {
		clone.Usage = new(r.Usage.clone())
	}
	clone.Extra = r.Extra.Clone()
	return &clone
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

// Response is one complete provider output with exactly one generation output.
type Response struct {
	Output   *Output           `json:"output,omitempty"`
	Metadata *ResponseMetadata `json:"metadata,omitempty"`
}

// NewResponse validates one complete output and its response-scoped metadata.
func NewResponse(output *Output, responseMetadata *ResponseMetadata) (*Response, error) {
	if output == nil {
		return nil, fmt.Errorf("chat: create response: %w: output must not be nil", ErrInvalidResponse)
	}
	response := &Response{Output: output, Metadata: responseMetadata}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("chat: create response: %w", err)
	}
	return response, nil
}

func (r *Response) Clone() *Response {
	if r == nil {
		return nil
	}
	clone := &Response{}
	if r.Output != nil {
		clone.Output = r.Output.clone()
	}
	if r.Metadata != nil {
		clone.Metadata = r.Metadata.clone()
	}
	return clone
}

func (r *Response) Text() string {
	if r == nil {
		return ""
	}
	return r.Output.Text()
}

func (r *Response) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil response", ErrInvalidResponse)
	}
	if r.Output == nil {
		return fmt.Errorf("%w: output must not be nil", ErrInvalidResponse)
	}
	if err := r.Output.Validate(); err != nil {
		return fmt.Errorf("%w: output: %w", ErrInvalidResponse, err)
	}
	if err := r.Metadata.validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidResponse, err)
	}
	return nil
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
