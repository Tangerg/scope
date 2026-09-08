package embedding

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/Tangerg/scope/core/metadata"
)

// Output is one embedding plus its metadata.
type Output struct {
	// Embedding is the vector representation of the input.
	Embedding []float64 `json:"embedding"`

	// Metadata carries provider-specific per-output extras.
	Metadata metadata.Map `json:"metadata,omitzero"`
}

// NewOutput validates and snapshots one provider result before it enters a Response.
func NewOutput(embedding []float64, outputMetadata metadata.Map) (*Output, error) {
	output := &Output{Embedding: slices.Clone(embedding), Metadata: outputMetadata.Clone()}
	if err := output.Validate(); err != nil {
		return nil, fmt.Errorf("embedding: create output: %w", err)
	}
	return output, nil
}

func (o *Output) Validate() error {
	if o == nil {
		return fmt.Errorf("%w: output must not be nil", ErrInvalidResponse)
	}
	if len(o.Embedding) == 0 {
		return fmt.Errorf("%w: embedding vector must not be empty", ErrInvalidResponse)
	}
	for i, value := range o.Embedding {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%w: embedding[%d] must be finite", ErrInvalidResponse, i)
		}
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
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode output: %w", ErrInvalidResponse, err)
	}
	candidate := Output(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*o = candidate
	return nil
}

// Usage records the token consumption an embedding request reported back.
// Embedding is input-only — there is no completion, reasoning, or cache
// dimension — so a single count is the whole story. Providers that report
// a "total" figure map it here: for embeddings every token is input.
type Usage struct {
	// InputTokens are tokens consumed embedding the inputs.
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
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode usage: %w", ErrInvalidResponse, err)
	}
	candidate := Usage(decoded)
	if err := candidate.validate(); err != nil {
		return err
	}
	*u = candidate
	return nil
}

// ResponseMetadata holds response-level metadata: the model actually
// used, token usage, creation time, and provider extras.
type ResponseMetadata struct {
	// Model is the model name actually served.
	Model string `json:"model"`

	// Usage breaks down token consumption. nil means the provider did not
	// report usage.
	Usage *Usage `json:"usage,omitempty"`

	// CreatedAt is the provider-reported creation timestamp.
	CreatedAt time.Time `json:"created_at,omitzero"`

	// Extra carries JSON-safe provider-specific metadata.
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
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode response metadata: %w", ErrInvalidResponse, err)
	}
	candidate := ResponseMetadata(decoded)
	if err := candidate.validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}

// Response is the full embedding output: one [*Output] per input plus
// shared response metadata.
type Response struct {
	// Outputs holds one entry per input text, in the same order.
	Outputs []*Output `json:"outputs,omitzero"`

	Metadata *ResponseMetadata `json:"metadata,omitempty"`
}

// NewResponse validates a complete provider result at the protocol boundary.
func NewResponse(outputs []*Output, responseMetadata *ResponseMetadata) (*Response, error) {
	response := &Response{Outputs: slices.Clone(outputs), Metadata: responseMetadata}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("embedding: create response: %w", err)
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
	dimensions := -1
	for i, output := range r.Outputs {
		if err := output.Validate(); err != nil {
			return fmt.Errorf("%w: outputs[%d]: %w", ErrInvalidResponse, i, err)
		}
		if dimensions < 0 {
			dimensions = len(output.Embedding)
		} else if len(output.Embedding) != dimensions {
			return fmt.Errorf("%w: outputs[%d]: dimensions = %d, want %d", ErrInvalidResponse, i, len(output.Embedding), dimensions)
		}
	}
	if err := r.Metadata.validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidResponse, err)
	}
	return nil
}

// ValidateFor checks a provider result against the request it answers.
//
// Outputs declares one entry per input text in the same order, and that
// correspondence is the whole basis for using an embedding: a response one
// vector short leaves every later text paired with its neighbor's vector, and
// nothing downstream can notice. Validate alone cannot see it, because the
// input count is not part of the response, so this is the check that makes the
// declared correspondence enforceable rather than aspirational.
func (r *Response) ValidateFor(request *Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if len(r.Outputs) != len(request.Texts) {
		return fmt.Errorf("%w: got %d outputs for %d input texts",
			ErrInvalidResponse, len(r.Outputs), len(request.Texts))
	}
	// A requested size is a promise about the vectors, not a hint. OpenAI
	// documents its dimensions parameter as "the number of dimensions the
	// resulting output embeddings should have" and Google documents
	// outputDimensionality as a reduced dimension where "excessive values in
	// the output embedding are truncated from the end" — both mean the vectors
	// come back at exactly that size. A model that does not support the
	// parameter would otherwise return its full-width vectors and the caller
	// would find out from whatever it fed them to, if at all. Validate has
	// already established that every output shares one size, so one comparison
	// answers for all of them.
	if request.Options.Dimensions != nil {
		want := *request.Options.Dimensions
		if got := int64(len(r.Outputs[0].Embedding)); got != want {
			return fmt.Errorf("%w: outputs have %d dimensions, but the request asked for %d",
				ErrInvalidResponse, got, want)
		}
	}
	return nil
}

// PlaceOutput stores one provider result at the position it belongs to.
//
// outputs must already be sized to the input count. Providers that tag each
// embedding with its own index may answer out of order, so placing by index is
// what restores the correspondence [Response.Outputs] declares; appending in
// arrival order silently pairs texts with the wrong vectors. An index outside
// the request and a position claimed twice are both rejected, and a position
// left unfilled fails when the Response is built.
func PlaceOutput(outputs []*Output, index int, embedding []float64, outputMetadata metadata.Map) error {
	if index < 0 || index >= len(outputs) {
		return fmt.Errorf("%w: output index %d is out of range for %d inputs",
			ErrInvalidResponse, index, len(outputs))
	}
	if outputs[index] != nil {
		return fmt.Errorf("%w: output index %d appears more than once",
			ErrInvalidResponse, index)
	}
	output, err := NewOutput(embedding, outputMetadata)
	if err != nil {
		return err
	}
	outputs[index] = output
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
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode response: %w", ErrInvalidResponse, err)
	}
	candidate := Response(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}
