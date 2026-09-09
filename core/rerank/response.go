package rerank

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// Score is a normalized relevance value in [0, 1].
type Score float64

func (s Score) Float64() float64 { return float64(s) }

func (s Score) Validate() error {
	value := float64(s)
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return fmt.Errorf("%w: score must be finite and in [0, 1], got %v", ErrInvalidResponse, value)
	}
	return nil
}

func (s Score) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	type wireScore Score
	return json.Marshal(wireScore(s))
}

func (s *Score) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: score receiver is nil", ErrInvalidResponse)
	}
	type wireScore Score
	var decoded wireScore
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode score: %w", ErrInvalidResponse, err)
	}
	candidate := Score(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// Result relates one input document index to its normalized relevance.
type Result struct {
	Index int   `json:"index"`
	Score Score `json:"score"`
}

// NewResult addresses a document by its index in the request rather than
// carrying the document itself. Repeating the text would let a response
// disagree with the request it answers, and would pay to move the corpus back
// across the wire.
func NewResult(index int, score Score) (*Result, error) {
	result := &Result{Index: index, Score: score}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("rerank: create result: %w", err)
	}
	return result, nil
}

func (r *Result) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: result must not be nil", ErrInvalidResponse)
	}
	if r.Index < 0 {
		return fmt.Errorf("%w: result index must not be negative", ErrInvalidResponse)
	}
	if err := r.Score.Validate(); err != nil {
		return err
	}
	return nil
}

func (r Result) MarshalJSON() ([]byte, error) {
	if err := (&r).Validate(); err != nil {
		return nil, err
	}
	type wireResult Result
	return json.Marshal(wireResult(r))
}

func (r *Result) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: result receiver is nil", ErrInvalidResponse)
	}
	type wireResult Result
	var decoded wireResult
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode result: %w", ErrInvalidResponse, err)
	}
	candidate := Result(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}

// Usage records input tokens when the provider reports them.
type Usage struct {
	InputTokens int64 `json:"input_tokens"`
}

func (u Usage) validate() error {
	if u.InputTokens < 0 {
		return fmt.Errorf("%w: input tokens must not be negative", ErrInvalidResponse)
	}
	return nil
}

func (u Usage) MarshalJSON() ([]byte, error) {
	if err := u.validate(); err != nil {
		return nil, err
	}
	type wireUsage Usage
	return json.Marshal(wireUsage(u))
}

func (u *Usage) UnmarshalJSON(data []byte) error {
	if u == nil {
		return fmt.Errorf("%w: usage receiver is nil", ErrInvalidResponse)
	}
	type wireUsage Usage
	var decoded wireUsage
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode usage: %w", ErrInvalidResponse, err)
	}
	candidate := Usage(decoded)
	if err := candidate.validate(); err != nil {
		return err
	}
	*u = candidate
	return nil
}

// ResponseMetadata records the served model, portable usage, and open
// provider response metadata.
type ResponseMetadata struct {
	Model string       `json:"model"`
	Usage *Usage       `json:"usage,omitempty"`
	Extra metadata.Map `json:"extra,omitzero"`
}

func (r *ResponseMetadata) validate() error {
	if r == nil {
		return nil
	}
	if r.Model != "" && strings.TrimSpace(r.Model) != r.Model {
		return fmt.Errorf("%w: response metadata model must not have surrounding whitespace", ErrInvalidResponse)
	}
	if r.Usage != nil {
		if err := r.Usage.validate(); err != nil {
			return err
		}
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

// Response is a relevance-descending subset of the input document indices.
type Response struct {
	Results  []*Result         `json:"results"`
	Metadata *ResponseMetadata `json:"metadata,omitempty"`
}

// NewResponse validates a complete provider result at the protocol boundary.
func NewResponse(results []*Result, responseMetadata *ResponseMetadata) (*Response, error) {
	response := &Response{Results: slices.Clone(results), Metadata: responseMetadata}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("rerank: create response: %w", err)
	}
	return response, nil
}

func (r *Response) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil response", ErrInvalidResponse)
	}
	if len(r.Results) == 0 {
		return fmt.Errorf("%w: at least one result is required", ErrInvalidResponse)
	}
	seen := make(map[int]struct{}, len(r.Results))
	for index, result := range r.Results {
		if err := result.Validate(); err != nil {
			return fmt.Errorf("%w: results[%d]: %w", ErrInvalidResponse, index, err)
		}
		if _, duplicate := seen[result.Index]; duplicate {
			return fmt.Errorf("%w: document index %d appears more than once", ErrInvalidResponse, result.Index)
		}
		seen[result.Index] = struct{}{}
		if index > 0 && r.Results[index-1].Score < result.Score {
			return fmt.Errorf("%w: results are not sorted by descending score at index %d", ErrInvalidResponse, index)
		}
	}
	if err := r.Metadata.validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidResponse, err)
	}
	return nil
}

func (r *Response) ValidateFor(request *Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	want := request.Options.ResultLimit(len(request.Documents))
	if len(r.Results) != want {
		return fmt.Errorf("%w: got %d results, want %d", ErrInvalidResponse, len(r.Results), want)
	}
	for position, result := range r.Results {
		if result.Index >= len(request.Documents) {
			return fmt.Errorf("%w: results[%d] index %d is out of range for %d documents", ErrInvalidResponse, position, result.Index, len(request.Documents))
		}
	}
	return nil
}

func (r *Response) First() *Result {
	if r == nil || len(r.Results) == 0 {
		return nil
	}
	return r.Results[0]
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
