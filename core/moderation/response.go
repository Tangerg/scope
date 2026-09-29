package moderation

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/Tangerg/scope/core/metadata"
)

// Verdict is one moderation dimension's outcome — a flagged bit plus a
// confidence score in [0, 1].
type Verdict struct {
	Flagged bool `json:"flagged"`

	// Score is the provider's confidence in the violation, 0–1.
	Score float64 `json:"score"`
}

func (v Verdict) validate() error {
	if math.IsNaN(v.Score) || math.IsInf(v.Score, 0) || v.Score < 0 || v.Score > 1 {
		return fmt.Errorf("%w: score must be finite and in [0, 1], got %v", ErrInvalidResponse, v.Score)
	}
	return nil
}

func (v Verdict) MarshalJSON() ([]byte, error) {
	if err := v.validate(); err != nil {
		return nil, err
	}
	type wireVerdict Verdict
	return jsonv2.Marshal(wireVerdict(v))
}

func (v *Verdict) UnmarshalJSON(data []byte) error {
	if v == nil {
		return fmt.Errorf("%w: verdict receiver is nil", ErrInvalidResponse)
	}
	type wireVerdict Verdict
	var decoded wireVerdict
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode verdict: %w", ErrInvalidResponse, err)
	}
	candidate := Verdict(decoded)
	if err := candidate.validate(); err != nil {
		return err
	}
	*v = candidate
	return nil
}

// Categories is the provider-reported category set. Keys retain provider
// semantics instead of forcing every provider through one closed taxonomy;
// Flagged collapses the open set only when a caller needs a yes/no policy gate.
type Categories map[string]Verdict

func (c Categories) Flagged() bool {
	for _, verdict := range c {
		if verdict.Flagged {
			return true
		}
	}
	return false
}

func (c Categories) validate() error {
	if len(c) == 0 {
		return fmt.Errorf("%w: categories must not be empty", ErrInvalidResponse)
	}
	for category, verdict := range c {
		if category == "" || strings.TrimSpace(category) != category {
			return fmt.Errorf("%w: invalid category %q", ErrInvalidResponse, category)
		}
		if err := verdict.validate(); err != nil {
			return fmt.Errorf("%w: category %q: %w", ErrInvalidResponse, category, err)
		}
	}
	return nil
}

func (c Categories) MarshalJSON() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	type wireCategories Categories
	return jsonv2.Marshal(wireCategories(c), jsonv2.Deterministic(true))
}

func (c *Categories) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("%w: categories receiver is nil", ErrInvalidResponse)
	}
	type wireCategories Categories
	var decoded wireCategories
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode categories: %w", ErrInvalidResponse, err)
	}
	candidate := Categories(decoded)
	if err := candidate.validate(); err != nil {
		return err
	}
	*c = candidate
	return nil
}

type Output struct {
	Categories Categories `json:"categories,omitzero"`

	Metadata metadata.Map `json:"metadata,omitzero"`
}

// NewOutput validates and snapshots one provider result before it enters a Response.
func NewOutput(categories Categories, outputMetadata metadata.Map) (*Output, error) {
	output := &Output{Categories: maps.Clone(categories), Metadata: outputMetadata.Clone()}
	if err := output.Validate(); err != nil {
		return nil, fmt.Errorf("moderation: create output: %w", err)
	}
	return output, nil
}

func (o *Output) Validate() error {
	if o == nil {
		return fmt.Errorf("%w: output must not be nil", ErrInvalidResponse)
	}
	if err := o.Categories.validate(); err != nil {
		return err
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
	return jsonv2.Marshal(wireOutput(o))
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

type ResponseMetadata struct {
	ID string `json:"id"`

	Model string `json:"model"`

	CreatedAt time.Time `json:"created_at,omitzero"`

	Extra metadata.Map `json:"extra,omitzero"`
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
	return jsonv2.Marshal(wireResponseMetadata(r))
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

type Response struct {
	// Outputs holds one entry per input, in the same order.
	Outputs []*Output `json:"outputs,omitzero"`

	Metadata *ResponseMetadata `json:"metadata,omitzero"`
}

func NewResponse(outputs []*Output, responseMetadata *ResponseMetadata) (*Response, error) {
	response := &Response{Outputs: slices.Clone(outputs), Metadata: responseMetadata}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("moderation: create response: %w", err)
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

// ValidateFor requires one output per input, in input order.
func (r *Response) ValidateFor(request *Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if len(r.Outputs) != len(request.Texts) {
		return fmt.Errorf("%w: got %d outputs for %d inputs",
			ErrInvalidResponse, len(r.Outputs), len(request.Texts))
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
	return jsonv2.Marshal(wireResponse(r))
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
